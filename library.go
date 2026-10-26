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
	faceCrop *faceCrop // immutable, set only in an isolated preview worker

	config           Config
	rootPath         string
	root             *os.Root
	db               *sql.DB
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
	var previous string
	err = db.QueryRow("SELECT value FROM settings WHERE key='root'").Scan(&previous)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		l.Close()
		return nil, err
	}
	if previous != "" && previous != rootPath {
		if _, err = db.Exec("DELETE FROM images; DELETE FROM locations"); err != nil {
			l.Close()
			return nil, err
		}
	}
	if _, err = db.Exec("INSERT OR REPLACE INTO settings VALUES('root',?)", rootPath); err != nil {
		l.Close()
		return nil, err
	}
	l.ai, err = newAI(l)
	if err != nil {
		l.Close()
		return nil, err
	}
	return l, nil
}
func (l *Library) Close() {
	if l.ai != nil {
		l.ai.Close()
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
	ticker := time.NewTicker(time.Duration(l.config.ScanInterval) * time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			l.Scan(ctx)
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
	l.mu.Lock()
	l.status.Scanning = true
	l.status.Errors = 0
	l.mu.Unlock()
	defer func() { l.mu.Lock(); l.status.Scanning = false; l.mu.Unlock() }()
	seen := map[string]bool{}
	complete := true
	err := fs.WalkDir(l.root.FS(), ".", func(path string, d fs.DirEntry, walkErr error) error {
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
		f, err := l.open(path)
		if err != nil {
			l.scanError(fmt.Errorf("open %s: %w", path, err))
			return nil
		}
		info, err := f.Stat()
		f.Close()
		if err != nil {
			complete = false
			l.scanError(err)
			return nil
		}
		signature := fmt.Sprintf("go1:%d:%d", info.Size(), info.ModTime().UnixNano())
		var previous string
		err = l.db.QueryRowContext(ctx, "SELECT signature FROM images WHERE path=?", path).Scan(&previous)
		if err != nil && !errors.Is(err, sql.ErrNoRows) {
			return err
		}
		if previous == signature {
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
		rows, err := l.db.QueryContext(ctx, "SELECT path FROM images")
		if err != nil {
			l.scanError(err)
			return
		}
		var stale []string
		for rows.Next() {
			var p string
			if err = rows.Scan(&p); err != nil {
				break
			}
			if !seen[p] {
				stale = append(stale, p)
			}
		}
		rowErr := rows.Err()
		rows.Close()
		if err != nil || rowErr != nil {
			l.scanError(fmt.Errorf("prune: %v %v", err, rowErr))
			return
		}
		for _, p := range stale {
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
	err := l.db.QueryRowContext(ctx, "SELECT metadata_json(metadata) FROM images WHERE id=?", id).Scan(&encoded)
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
