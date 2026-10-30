package main

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"log"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	_ "modernc.org/sqlite"
)

type Photo struct {
	ID            string         `json:"id"`
	Path          string         `json:"path"`
	AbsolutePath  string         `json:"absolute_path"`
	Filename      string         `json:"filename"`
	Directory     string         `json:"directory"`
	Size          int64          `json:"size_bytes"`
	Width         int            `json:"width"`
	Height        int            `json:"height"`
	Format        string         `json:"format"`
	CreatedAt     *string        `json:"created_at"`
	ModifiedAt    string         `json:"modified_at"`
	TakenAt       *string        `json:"taken_at"`
	TakenAtOffset *string        `json:"taken_at_offset"`
	Exif          map[string]any `json:"exif"`
	ImageURL      string         `json:"image_url"`
	OriginalURL   string         `json:"original_url"`
	Orientation   int            `json:"-"`
}
type scanStatus struct {
	Scanning bool    `json:"scanning"`
	LastScan *string `json:"last_scan"`
	Errors   int     `json:"errors"`
}
type Library struct {
	backup   *phoneBackup
	faceCrop *faceCrop // immutable, set only in an isolated preview worker

	config           Config
	rootPath         string
	root             *os.Root
	db               *sql.DB
	read             *sql.DB // Foreground reads never queue behind catalog maintenance.
	mu               sync.RWMutex
	status           scanStatus
	scanMu           sync.Mutex
	renderSlots      chan struct{}
	thumbnails       *thumbnailCache
	previewDecoder   chan struct{}
	isolateRendering bool
	ai               *AI
	lastRender       atomic.Int64
}

func newLibrary(c Config) (*Library, error) {
	rootPath, err := filepath.EvalSymlinks(c.Root)
	if err != nil {
		return nil, err
	}
	rootPath, err = filepath.Abs(rootPath)
	if err != nil {
		return nil, err
	}
	r, err := os.OpenRoot(rootPath)
	if err != nil {
		return nil, err
	}
	if err = os.MkdirAll(c.DataDir, 0750); err != nil {
		r.Close()
		return nil, err
	}
	db := openSQLite(filepath.Join(c.DataDir, "index-go.sqlite3"), map[string]string{
		"curation": filepath.Join(c.DataDir, "curation.sqlite3"),
	}, false)
	l := &Library{config: c, rootPath: rootPath, root: r, db: db, renderSlots: make(chan struct{}, 2), thumbnails: newThumbnailCache(c.DataDir), previewDecoder: make(chan struct{}, 1)}
	_, err = db.Exec(`PRAGMA journal_mode=WAL; PRAGMA synchronous=NORMAL; PRAGMA busy_timeout=10000;
CREATE TABLE IF NOT EXISTS images(id TEXT PRIMARY KEY,path TEXT UNIQUE NOT NULL,directory TEXT NOT NULL,signature TEXT NOT NULL,metadata TEXT NOT NULL,search TEXT NOT NULL);
CREATE INDEX IF NOT EXISTS image_path ON images(path COLLATE NOCASE);
CREATE INDEX IF NOT EXISTS image_order ON images(path COLLATE NOCASE,path,id,directory);
CREATE TABLE IF NOT EXISTS settings(key TEXT PRIMARY KEY,value TEXT);
CREATE TABLE IF NOT EXISTS locations(image_id TEXT PRIMARY KEY,signature TEXT NOT NULL,label TEXT NOT NULL);`)
	if err != nil {
		l.Close()
		return nil, err
	}
	if err = migrateCatalog(db); err != nil {
		l.Close()
		return nil, err
	}
	if err = ensureImageIdentitySchema(db); err != nil {
		l.Close()
		return nil, err
	}
	if err = ensurePhotoSortIndexes(db); err != nil {
		l.Close()
		return nil, err
	}
	// Cover grouping/autocomplete fields without scanning compressed metadata pages.
	if _, err = db.Exec("CREATE INDEX IF NOT EXISTS image_person_capture ON images(id,signature,taken_at); CREATE INDEX IF NOT EXISTS image_directory_exact ON images(directory,id,signature); CREATE INDEX IF NOT EXISTS image_group_fields_v2 ON images(directory COLLATE NOCASE,path,taken_at,created_at,size_bytes,id,signature); DROP INDEX IF EXISTS image_group_fields"); err != nil {
		l.Close()
		return nil, err
	}
	if _, err = db.Exec(`CREATE INDEX IF NOT EXISTS photo_group_path ON images(directory COLLATE NOCASE,path COLLATE NOCASE,path,id);
 CREATE INDEX IF NOT EXISTS photo_group_year ON images(coalesce(nullif(substr(coalesce(nullif(taken_at,''),created_at),1,4),''),'Unknown year') COLLATE NOCASE,path COLLATE NOCASE,path,id);`); err != nil {
		l.Close()
		return nil, err
	}
	var previous string
	err = db.QueryRow("SELECT value FROM settings WHERE key='root'").Scan(&previous)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		l.Close()
		return nil, err
	}
	if previous != "" && previous != rootPath {
		if _, err = db.Exec("DELETE FROM images; DELETE FROM locations; DELETE FROM phone_backup_hashes"); err != nil {
			l.Close()
			return nil, err
		}
	}
	if _, err = db.Exec("INSERT OR REPLACE INTO settings VALUES('root',?)", rootPath); err != nil {
		l.Close()
		return nil, err
	}
	l.read = openSQLite(filepath.Join(c.DataDir, "index-go.sqlite3"), map[string]string{"curation": filepath.Join(c.DataDir, "curation.sqlite3")}, true)
	l.read.SetMaxOpenConns(4)
	l.ai, err = newAI(l)
	if err != nil {
		l.Close()
		return nil, err
	}
	l.backup, err = newPhoneBackup(l)
	if err != nil {
		l.Close()
		return nil, err
	}
	// Gather bounded planner statistics after schema changes, before the reader
	// pools prepare their first queries. Maintenance is best effort.
	l.optimizeDatabases(context.Background())
	return l, nil
}

func (l *Library) optimizeDatabases(ctx context.Context) {
	ctx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	for _, db := range []*sql.DB{l.db, l.ai.db} {
		// Readers and writers use different connections. Inspect all tables,
		// including those this writer connection has not queried itself.
		if _, err := db.ExecContext(ctx, "PRAGMA main.optimize=0x10002"); err != nil && ctx.Err() == nil {
			log.Printf("SQLite planner maintenance: %v", err)
		}
	}
}
func (l *Library) Close() {
	if l.backup != nil {
		l.backup.Close()
	}
	if l.ai != nil {
		l.ai.Close()
	}
	if l.read != nil {
		_ = l.read.Close()
	}
	_ = l.db.Close()
	_ = l.root.Close()
}
func (l *Library) Status() scanStatus  { l.mu.RLock(); defer l.mu.RUnlock(); return l.status }
func (l *Library) scanError(err error) { l.mu.Lock(); l.status.Errors++; l.mu.Unlock(); log.Print(err) }

type apiError struct {
	code int
	text string
}

func (e *apiError) Error() string     { return e.text }
func bad(code int, text string) error { return &apiError{code, text} }

func cleanRelative(relative string) (string, error) {
	if strings.ContainsAny(relative, ":\\\x00") || strings.HasPrefix(relative, "/") {
		return "", bad(400, "Use a root-relative path with forward slashes")
	}
	for _, part := range strings.Split(relative, "/") {
		if part == ".." {
			return "", bad(400, "Parent traversal is not allowed")
		}
	}
	return filepath.FromSlash(filepath.ToSlash(filepath.Clean(relative))), nil
}
func (l *Library) open(relative string) (*os.File, error) {
	name, err := cleanRelative(relative)
	if err != nil {
		return nil, err
	}
	f, err := l.root.Open(name)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, bad(404, "Path not found")
		}
		return nil, bad(403, "Path cannot be read or is outside the photo root")
	}
	return f, nil
}
func (l *Library) directory(relative string) (string, error) {
	f, err := l.open(relative)
	if err != nil {
		return "", err
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil {
		return "", err
	}
	if !info.IsDir() {
		return "", bad(400, "Not a directory")
	}
	name, _ := cleanRelative(relative)
	if name == "." {
		return "", nil
	}
	return filepath.ToSlash(name), nil
}
func (l *Library) Run(ctx context.Context) {
	watchDone := make(chan struct{})
	go func() { defer close(watchDone); l.watchChanges(ctx) }()
	defer func() { <-watchDone }()
	identityDone := make(chan struct{})
	go func() { defer close(identityDone); l.backfillImageIdentities(ctx) }()
	defer func() { <-identityDone }()
	summariesDone := make(chan struct{})
	go func() { defer close(summariesDone); l.ai.warmPeopleSummaries(ctx) }()
	defer func() { <-summariesDone }()
	peopleDone := make(chan struct{})
	go func() { defer close(peopleDone); l.warmPeopleThumbnails(ctx) }()
	defer func() { <-peopleDone }()
	geoDone := make(chan struct{})
	go func() { defer close(geoDone); l.geocode(ctx) }()
	defer func() { <-geoDone }()
	aiDone := make(chan struct{})
	go func() { defer close(aiDone); l.ai.Run(ctx) }()
	thumbnailDone := make(chan struct{})
	go func() { defer close(thumbnailDone); l.warmThumbnails(ctx) }()
	defer func() { <-thumbnailDone }()
	defer func() { <-aiDone }()
	l.Scan(ctx)
	l.optimizeDatabases(ctx)
	ticker := time.NewTicker(time.Duration(l.config.ScanInterval) * time.Second)
	defer ticker.Stop()
	optimizeTicker := time.NewTicker(24 * time.Hour)
	defer optimizeTicker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			l.Scan(ctx)
		case <-optimizeTicker.C:
			l.optimizeDatabases(ctx)
		}
	}
}
func supported(path string) bool {
	switch strings.ToLower(filepath.Ext(path)) {
	case ".jpg", ".jpeg", ".png", ".gif", ".bmp", ".tif", ".tiff", ".webp", ".heic", ".heif":
		return true
	}
	return false
}
func isHEIC(path string) bool {
	e := strings.ToLower(filepath.Ext(path))
	return e == ".heic" || e == ".heif"
}

func (l *Library) Scan(ctx context.Context) {
	if !l.scanMu.TryLock() {
		return
	}
	defer l.scanMu.Unlock()
	l.scanLocked(ctx)
}

func (l *Library) scanLocked(ctx context.Context) {
	l.mu.Lock()
	l.status.Scanning = true
	l.status.Errors = 0
	l.mu.Unlock()
	defer func() { l.mu.Lock(); l.status.Scanning = false; l.mu.Unlock() }()
	// Read the compact catalog once instead of issuing a query for each file.
	known := make(map[string]string)
	rows, err := l.db.QueryContext(ctx, "SELECT path,signature FROM images")
	if err != nil {
		l.scanError(err)
		return
	}
	for rows.Next() {
		var path, signature string
		if err = rows.Scan(&path, &signature); err != nil {
			break
		}
		known[path] = signature
	}
	rowErr := rows.Err()
	rows.Close()
	if err != nil || rowErr != nil {
		l.scanError(fmt.Errorf("read catalog: %v %v", err, rowErr))
		return
	}
	seen := map[string]bool{}
	complete := true
	err = fs.WalkDir(l.root.FS(), ".", func(path string, d fs.DirEntry, walkErr error) error {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if walkErr != nil {
			complete = false
			l.scanError(fmt.Errorf("walk %s: %w", path, walkErr))
			return nil
		}
		// Do not follow symlinks or junctions while indexing.
		if d.Type()&os.ModeSymlink != 0 {
			if d.IsDir() {
				return fs.SkipDir
			}
			return nil
		}
		if d.IsDir() || !supported(path) {
			return nil
		}
		seen[path] = true
		// Directory entries on Windows already contain size and modification time.
		info, err := d.Info()
		if err != nil {
			complete = false
			l.scanError(err)
			return nil
		}
		signature := fmt.Sprintf("go1:%d:%d", info.Size(), info.ModTime().UnixNano())
		if known[path] == signature {
			return nil
		}
		photo, err := l.metadata(ctx, path)
		if err != nil {
			l.scanError(fmt.Errorf("index %s: %w", path, err))
			_, dbErr := l.db.ExecContext(ctx, "DELETE FROM images WHERE path=?", path)
			return dbErr
		}
		encoded, err := json.Marshal(photo)
		if err != nil {
			l.scanError(err)
			return nil
		}
		_, err = l.db.ExecContext(ctx, "INSERT OR REPLACE INTO images(id,path,directory,signature,metadata,search) VALUES(?,?,?,?,metadata_pack(?),metadata_search(?))", photo.ID, path, photo.Directory, signature, string(encoded), string(encoded))
		return err
	})
	if err != nil {
		complete = false
		if !errors.Is(err, context.Canceled) {
			l.scanError(err)
		}
	}
	if complete {
		for p := range known {
			if seen[p] {
				continue
			}
			if _, err = l.db.ExecContext(ctx, "DELETE FROM images WHERE path=?", p); err != nil {
				l.scanError(err)
				return
			}
		}
		now := time.Now().UTC().Format(time.RFC3339Nano)
		l.mu.Lock()
		l.status.LastScan = &now
		l.mu.Unlock()
	}
	log.Printf("Scan finished: complete=%t errors=%d", complete, l.Status().Errors)
}
func imageID(path string) string { s := sha256.Sum256([]byte(path)); return hex.EncodeToString(s[:12]) }
func (l *Library) get(ctx context.Context, id string) (Photo, error) {
	var encoded string
	err := l.read.QueryRowContext(ctx, "SELECT metadata_json(metadata) FROM images WHERE id=?", id).Scan(&encoded)
	if errors.Is(err, sql.ErrNoRows) {
		return Photo{}, bad(404, "Unknown image")
	}
	if err != nil {
		return Photo{}, err
	}
	var p Photo
	if err = json.Unmarshal([]byte(encoded), &p); err != nil {
		return p, err
	}
	f, err := l.open(p.Path)
	if err != nil {
		return p, err
	}
	f.Close()
	return p, nil
}
