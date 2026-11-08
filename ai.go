package main

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

type aiPipelineStats struct {
	inFlight    atomic.Int64
	prepared    atomic.Int64
	completed   atomic.Int64
	previewUS   atomic.Int64
	inferenceUS atomic.Int64
	inferred    atomic.Int64
	saveUS      atomic.Int64
	waitUS      atomic.Int64
	started     atomic.Int64
}
type AI struct {
	videoWorked         bool
	personIndexMu       sync.Mutex
	personIndex         *personHNSW
	peopleMu            sync.Mutex
	peopleSummary       *peopleSnapshot
	peopleBuilding      chan struct{}
	statusMu            sync.Mutex
	statusCounts        indexCounts
	statusCountedAt     time.Time
	statusRevision      atomic.Uint64
	statusCountRevision uint64
	pipeline            aiPipelineStats
	lib                 *Library
	db, read            *sql.DB // Independent WAL database; never hold catalog connections during inference.
	ml                  *mlClient
	models              string
	paused              atomic.Bool
	mu                  sync.Mutex
	active              string
	lastError           string
	lastSuccess         string
	online              bool
	textCache           map[string]vector
	groupCache          map[string]photoGroupSummary
	searchSlot          chan struct{}
	suggestionSlot      chan struct{}
	playMu              sync.Mutex
	playCache           map[string]playSelection
	selectionRevision   atomic.Uint64
	// editRevision counts user edits only; indexing never bumps it, so
	// playback can drop snapshots immediately on edits yet keep serving
	// them while background indexing keeps changing selectionRevision.
	editRevision   atomic.Uint64
	refreshCtx     context.Context
	refreshCancel  context.CancelFunc
	refreshWG      sync.WaitGroup
	playRefreshing bool
}

func newAI(l *Library) (*AI, error) {
	l.config.ML.defaults()
	a := &AI{lib: l, ml: newML(l.config.ML), models: l.config.ML.ClipModel + "|" + l.config.ML.FaceModel, textCache: map[string]vector{}, searchSlot: make(chan struct{}, 1), suggestionSlot: make(chan struct{}, 1)}
	a.refreshCtx, a.refreshCancel = context.WithCancel(context.Background())
	var err error
	attachments := map[string]string{
		"catalog":  filepath.Join(l.config.DataDir, "index-go.sqlite3"),
		"curation": filepath.Join(l.config.DataDir, "curation.sqlite3"),
	}
	a.db = openSQLite(filepath.Join(l.config.DataDir, "ai.sqlite3"), attachments, false)
	fail := func(err error) (*AI, error) { a.Close(); return nil, err }
	_, err = a.db.Exec(`PRAGMA journal_mode=WAL; PRAGMA synchronous=NORMAL; PRAGMA busy_timeout=5000;
 CREATE TABLE IF NOT EXISTS ai_images(image_id TEXT PRIMARY KEY,signature TEXT NOT NULL,models TEXT NOT NULL,clip BLOB,error TEXT NOT NULL DEFAULT '',retry_at INTEGER NOT NULL DEFAULT 0,updated_at TEXT);
 CREATE INDEX IF NOT EXISTS ai_status ON ai_images(models,image_id,signature,error);
 CREATE TABLE IF NOT EXISTS people(id INTEGER PRIMARY KEY,name TEXT NOT NULL DEFAULT '',model TEXT NOT NULL,embedding BLOB NOT NULL,merged_into INTEGER);
 CREATE INDEX IF NOT EXISTS people_merged_into ON people(merged_into);
 CREATE INDEX IF NOT EXISTS people_name ON people(model,name COLLATE NOCASE) WHERE merged_into IS NULL;
 CREATE TABLE IF NOT EXISTS faces(id INTEGER PRIMARY KEY,image_id TEXT NOT NULL,person_id INTEGER NOT NULL,box TEXT NOT NULL,score REAL NOT NULL,width INTEGER NOT NULL,height INTEGER NOT NULL);
 CREATE INDEX IF NOT EXISTS face_image ON faces(image_id); CREATE INDEX IF NOT EXISTS face_person ON faces(person_id,image_id);
 CREATE INDEX IF NOT EXISTS face_person_cover ON faces(person_id,score DESC,id);
 CREATE TABLE IF NOT EXISTS face_overrides(image_id TEXT NOT NULL,signature TEXT NOT NULL,model TEXT NOT NULL,box TEXT NOT NULL,person_id INTEGER NOT NULL,PRIMARY KEY(image_id,signature,model,box));
 CREATE TABLE IF NOT EXISTS person_dismissals(a INTEGER NOT NULL,b INTEGER NOT NULL,PRIMARY KEY(a,b));
 CREATE TABLE IF NOT EXISTS ai_settings(key TEXT PRIMARY KEY,value TEXT NOT NULL);`)
	if err != nil {
		return fail(err)
	}
	if err = a.initPersonHNSW(); err != nil {
		return fail(err)
	}
	if _, err = a.db.Exec(videoSchema); err != nil {
		return fail(err)
	}
	// Per-face embeddings let a manually retagged face teach recognition.
	// Faces indexed before this column existed keep NULL.
	var hasEmbedding int
	if err = a.db.QueryRow("SELECT count(*) FROM pragma_table_info('faces') WHERE name='embedding'").Scan(&hasEmbedding); err != nil {
		return fail(err)
	}
	if hasEmbedding == 0 {
		if _, err = a.db.Exec("ALTER TABLE faces ADD COLUMN embedding BLOB"); err != nil {
			return fail(err)
		}
	}
	var root string
	err = a.db.QueryRow("SELECT value FROM ai_settings WHERE key='root'").Scan(&root)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return fail(err)
	}
	if root != "" && root != l.rootPath && l.config.RelocateFrom != root {
		if _, err = a.db.Exec("DELETE FROM video_frames;DELETE FROM faces;DELETE FROM people;DELETE FROM ai_images;DELETE FROM person_dismissals;DELETE FROM face_overrides;DELETE FROM ai_settings WHERE key='merge_undo';"); err != nil {
			return fail(err)
		}
	}
	if _, err = a.db.Exec("INSERT OR REPLACE INTO ai_settings VALUES('root',?)", l.rootPath); err != nil {
		return fail(err)
	}
	var paused string
	_ = a.db.QueryRow("SELECT value FROM ai_settings WHERE key='paused'").Scan(&paused)
	a.paused.Store(paused == "true")
	a.read = openSQLite(filepath.Join(l.config.DataDir, "ai.sqlite3"), attachments, true)
	a.read.SetMaxOpenConns(4)
	if err := a.initCuration(); err != nil {
		return fail(err)
	}
	if l.config.RelocateFrom != "" && l.config.RelocateFrom != l.rootPath {
		root = l.config.RelocateFrom
		tx, e := a.db.Begin()
		if e != nil {
			return fail(e)
		}
		for _, table := range []string{"annotations", "albums", "collections"} {
			if _, e = tx.Exec("UPDATE curation."+table+" SET root=? WHERE root=?", l.rootPath, root); e != nil {
				tx.Rollback()
				return fail(e)
			}
		}
		if e = tx.Commit(); e != nil {
			return fail(e)
		}
	}

	a.warmPersonHNSW()
	return a, nil
}
func (a *AI) Close() {
	if a.refreshCancel != nil {
		a.refreshCancel()
		a.refreshWG.Wait()
	}
	a.flushPersonHNSW()
	if a.ml != nil {
		a.ml.http.CloseIdleConnections()
	}
	if a.read != nil {
		_ = a.read.Close()
	}
	if a.db != nil {
		_ = a.db.Close()
	}
}
func waitContext(ctx context.Context, d time.Duration) bool {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-t.C:
		return true
	}
}

type aiJob struct{ ID, Signature, Metadata string }

// Finding pending work scans the whole catalog, which takes seconds on a large
// library. Run it on the read pool so it never holds the single write
// connection that tag edits and other mutations wait on.
func (a *AI) jobs(ctx context.Context, after string) ([]aiJob, error) {
	rows, err := a.read.QueryContext(ctx, `SELECT i.id,i.signature,metadata_json(i.metadata) FROM catalog.images i LEFT JOIN ai_images a ON a.image_id=i.id
 WHERE `+photoOnlySQL+` AND i.id>? AND (a.image_id IS NULL OR a.signature!=i.signature OR a.models!=? OR (a.error!='' AND a.retry_at<=?)) ORDER BY i.id LIMIT 32`, after, a.models, time.Now().Unix())
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var jobs []aiJob
	for rows.Next() {
		var j aiJob
		if err := rows.Scan(&j.ID, &j.Signature, &j.Metadata); err != nil {
			return nil, err
		}
		jobs = append(jobs, j)
	}
	return jobs, rows.Err()
}
func (a *AI) setError(err error) {
	a.mu.Lock()
	a.lastError = err.Error()
	a.mu.Unlock()
	log.Printf("AI: %v", err)
}
func (a *AI) waitIndexing(ctx context.Context, foreground bool) bool {
	start := time.Now()
	defer func() { a.pipeline.waitUS.Add(time.Since(start).Microseconds()) }()
	for a.paused.Load() || (foreground && (a.lib.renderSlots.active() > 0 || time.Since(time.Unix(0, a.lib.lastRender.Load())) < 200*time.Millisecond)) {
		if !waitContext(ctx, 50*time.Millisecond) {
			return false
		}
	}
	return ctx.Err() == nil
}
func (a *AI) Run(ctx context.Context) {
	if !a.lib.config.ML.Enabled || a.lib.config.ML.URL == "" {
		return
	}
	if !waitContext(ctx, 5*time.Second) {
		return
	}
	a.pipeline.started.Store(time.Now().UnixNano())
	cursor := ""
	for ctx.Err() == nil {
		if !a.waitIndexing(ctx, true) {
			return
		}
		jobs, err := a.jobs(ctx, cursor)
		if err != nil {
			a.setError(err)
			if !waitContext(ctx, 30*time.Second) {
				return
			}
			continue
		}
		if len(jobs) == 0 {
			// Restart the photo scan before spending time on lower-priority videos.
			// A scan that already started from the beginning needs no repeat.
			if cursor != "" {
				photos, e := a.jobs(ctx, "")
				if e != nil {
					a.setError(e)
					continue
				}
				if len(photos) > 0 {
					cursor = ""
					continue
				}
			}
			if e := a.indexNextVideo(ctx); e != nil {
				a.setError(e)
				if !waitContext(ctx, 30*time.Second) {
					return
				}
			} else if a.videoWorked {
				cursor = ""
				continue
			}
			cursor = ""
			a.cleanup(ctx)
			if !waitContext(ctx, 30*time.Second) {
				return
			}
			continue
		}
		err = runAIPipeline(ctx, jobs, a.lib.config.ML.Concurrency,
			func(batch context.Context, j aiJob) ([]byte, error) {
				if !a.waitIndexing(batch, true) {
					return nil, batch.Err()
				}
				var p Photo
				if err := json.Unmarshal([]byte(j.Metadata), &p); err != nil {
					return nil, err
				}
				a.mu.Lock()
				a.active = p.Path
				a.mu.Unlock()
				start := time.Now()
				preview, err := a.lib.aiPreview(batch, p)
				a.pipeline.previewUS.Add(time.Since(start).Microseconds())
				a.pipeline.prepared.Add(1)
				return preview, err
			},
			func(batch context.Context, preview []byte) (mlPrediction, error) {
				if !a.waitIndexing(batch, false) {
					return mlPrediction{}, batch.Err()
				}
				a.pipeline.inFlight.Add(1)
				defer a.pipeline.inFlight.Add(-1)
				start := time.Now()
				prediction, err := a.ml.predict(batch, preview, "")
				a.pipeline.inferenceUS.Add(time.Since(start).Microseconds())
				a.pipeline.inferred.Add(1)
				return prediction, err
			},
			func(result aiPipelineResult[aiJob]) error {
				if result.inferenceError {
					a.mu.Lock()
					a.online = false
					a.mu.Unlock()
					a.setError(result.err)
					return result.err
				}
				if result.err != nil {
					a.recordFailure(ctx, result.job, result.err)
					return nil
				}
				start := time.Now()
				err := a.save(ctx, result.job, result.prediction)
				a.pipeline.saveUS.Add(time.Since(start).Microseconds())
				if err != nil {
					a.recordFailure(ctx, result.job, err)
				} else {
					var photo Photo
					if json.Unmarshal([]byte(result.job.Metadata), &photo) == nil {
						a.lib.seedFaceThumbnails(ctx, photo, result.preview, result.prediction, result.job.Signature)
					}
					a.pipeline.completed.Add(1)
					a.mu.Lock()
					a.online = true
					a.lastError = ""
					a.lastSuccess = time.Now().UTC().Format(time.RFC3339)
					a.mu.Unlock()
				}
				return nil
			})
		a.mu.Lock()
		a.active = ""
		a.mu.Unlock()
		if ctx.Err() != nil {
			return
		}
		if err != nil {
			if !waitContext(ctx, 30*time.Second) {
				return
			}
			continue
		}
		cursor = jobs[len(jobs)-1].ID
	}
}

func (a *AI) recordFailure(ctx context.Context, j aiJob, e error) {
	a.setError(e)
	_, err := a.db.ExecContext(ctx, `INSERT INTO ai_images(image_id,signature,models,error,retry_at) VALUES(?,?,?,?,?) ON CONFLICT(image_id) DO UPDATE SET signature=excluded.signature,models=excluded.models,clip=NULL,error=excluded.error,retry_at=excluded.retry_at`, j.ID, j.Signature, a.models, e.Error(), time.Now().Add(time.Hour).Unix())
	if err != nil && ctx.Err() == nil {
		a.setError(err)
	}
}
func (a *AI) cleanup(ctx context.Context) {
	// This runs every idle cycle. Look for orphans on the read pool first so
	// the write connection is only taken when there is something to delete.
	var orphaned bool
	err := a.read.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM video_frames WHERE image_id NOT IN (SELECT id FROM catalog.images UNION SELECT id FROM catalog.image_tombstones)) OR EXISTS(SELECT 1 FROM faces WHERE image_id NOT IN (SELECT id FROM catalog.images UNION SELECT id FROM catalog.image_tombstones)) OR EXISTS(SELECT 1 FROM ai_images WHERE image_id NOT IN (SELECT id FROM catalog.images UNION SELECT id FROM catalog.image_tombstones))`).Scan(&orphaned)
	if err != nil {
		if ctx.Err() == nil {
			a.setError(err)
		}
		return
	}
	if !orphaned {
		return
	}
	_, err = a.db.ExecContext(ctx, `DELETE FROM video_frames WHERE image_id NOT IN (SELECT id FROM catalog.images UNION SELECT id FROM catalog.image_tombstones); DELETE FROM faces WHERE image_id NOT IN (SELECT id FROM catalog.images UNION SELECT id FROM catalog.image_tombstones); DELETE FROM ai_images WHERE image_id NOT IN (SELECT id FROM catalog.images UNION SELECT id FROM catalog.image_tombstones);`)
	if err != nil && ctx.Err() == nil {
		a.setError(err)
	}
}
func (a *AI) save(ctx context.Context, j aiJob, p mlPrediction) error {
	clip, err := parseVector(p.Clip)
	if err != nil {
		return fmt.Errorf("CLIP: %w", err)
	}
	if p.Width < 1 || p.Height < 1 || len(p.Faces) > 1000 {
		return fmt.Errorf("invalid ML image dimensions or excessive faces")
	}
	// Detectors can predict boxes partially outside a preview. Clip for display;
	// an edge face must not prevent the photo's content embedding being indexed.
	faces := make([]mlFace, 0, len(p.Faces))
	for _, f := range p.Faces {
		f.Box.X1 = max(0, min(float64(p.Width), f.Box.X1))
		f.Box.X2 = max(0, min(float64(p.Width), f.Box.X2))
		f.Box.Y1 = max(0, min(float64(p.Height), f.Box.Y1))
		f.Box.Y2 = max(0, min(float64(p.Height), f.Box.Y2))
		if f.Box.X2 > f.Box.X1 && f.Box.Y2 > f.Box.Y1 {
			faces = append(faces, f)
		}
	}
	p.Faces = faces
	vectors := make([]vector, len(p.Faces))
	for i, f := range p.Faces {
		vectors[i], err = parseVector(f.Embedding)
		if err != nil {
			return err
		}
	}
	// A source changed during inference: discard this result and let the next scan enqueue it.
	var signature string
	if err = a.lib.db.QueryRowContext(ctx, "SELECT signature FROM images WHERE id=?", j.ID).Scan(&signature); err != nil {
		return err
	}
	if signature != j.Signature {
		return nil
	}
	tx, err := a.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	rows, err := tx.QueryContext(ctx, "SELECT coalesce(merged_into,id),embedding FROM people WHERE model=?", a.lib.config.ML.FaceModel)
	if err != nil {
		return err
	}
	type personVector struct {
		id int64
		v  vector
	}
	var people []personVector
	for rows.Next() {
		var id int64
		var blob []byte
		if err = rows.Scan(&id, &blob); err != nil {
			rows.Close()
			return err
		}
		people = append(people, personVector{id, vectorBlob(blob)})
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return err
	}
	if _, err = tx.ExecContext(ctx, "DELETE FROM faces WHERE image_id=?", j.ID); err != nil {
		return err
	}
	used := map[int64]bool{} // Two different faces in a photo cannot silently become one person.
	for i, f := range p.Faces {
		if f.Score < 0.8 {
			continue
		}
		best, id := a.lib.config.ML.FaceSimilarity, int64(0)
		if f.Timestamp != nil {
			best = max(best, .7)
		}
		for _, person := range people {
			if !used[person.id] {
				if s := similarity(vectors[i], person.v); s > best {
					best, id = s, person.id
				}
			}
		}
		box := faceBoxJSON(f)
		var override int64
		e := tx.QueryRowContext(ctx, `SELECT coalesce(p.merged_into,p.id) FROM face_overrides o JOIN people p ON p.id=o.person_id WHERE o.image_id=? AND o.signature=? AND o.model=? AND o.box=?`, j.ID, j.Signature, a.lib.config.ML.FaceModel, string(box)).Scan(&override)
		if e == nil {
			id = override
		} else if !errors.Is(e, sql.ErrNoRows) {
			return e
		}
		if id == 0 {
			result, e := tx.ExecContext(ctx, "INSERT INTO people(model,embedding) VALUES(?,?)", a.lib.config.ML.FaceModel, vectors[i].blob())
			if e != nil {
				return e
			}
			id, err = result.LastInsertId()
			if err != nil {
				return err
			}
			people = append(people, personVector{id, vectors[i]})
		}
		used[id] = true
		if _, err = tx.ExecContext(ctx, "INSERT INTO faces(image_id,person_id,box,score,width,height,embedding) VALUES(?,?,?,?,?,?,?)", j.ID, id, string(box), f.Score, p.Width, p.Height, vectors[i].blob()); err != nil {
			return err
		}
	}
	if _, err = tx.ExecContext(ctx, `INSERT INTO ai_images(image_id,signature,models,clip,error,retry_at,updated_at) VALUES(?,?,?,?,'',0,?) ON CONFLICT(image_id) DO UPDATE SET signature=excluded.signature,models=excluded.models,clip=excluded.clip,error='',retry_at=0,updated_at=excluded.updated_at`, j.ID, j.Signature, a.models, clip.blob(), time.Now().UTC().Format(time.RFC3339)); err != nil {
		return err
	}
	return tx.Commit()
}
func (a *AI) textVector(ctx context.Context, q string) (vector, error) {
	q = strings.TrimSpace(q)
	a.mu.Lock()
	v := a.textCache[q]
	a.mu.Unlock()
	if v != nil {
		return v, nil
	}
	// Interactive searches must not inherit the three-minute image inference
	// timeout, including time waiting for a busy worker connection.
	ctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	pred, err := a.ml.predict(ctx, nil, q)
	if err != nil {
		return nil, bad(503, "Content search could not reach the ML worker or it took too long. Try again when the worker is ready; your face filters are unchanged.")
	}
	v, err = parseVector(pred.Clip)
	if err != nil {
		return nil, err
	}
	a.mu.Lock()
	if len(a.textCache) >= 64 {
		a.textCache = map[string]vector{}
	}
	a.textCache[q] = v
	a.mu.Unlock()
	return v, nil
}
