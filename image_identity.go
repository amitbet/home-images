package main

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"time"
)

func ensureImageIdentitySchema(db *sql.DB) error {
	tx, err := db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	for _, column := range []struct{ name, kind string }{{"content_id", "TEXT"}, {"file_hash", "TEXT"}, {"hash_signature", "TEXT"}, {"hash_error", "TEXT"}, {"hash_retry_at", "INTEGER NOT NULL DEFAULT 0"}} {
		var count int
		if err = tx.QueryRow("SELECT count(*) FROM pragma_table_info('images') WHERE name=?", column.name).Scan(&count); err != nil {
			return err
		}
		if count == 0 {
			if _, err = tx.Exec("ALTER TABLE images ADD COLUMN " + column.name + " " + column.kind); err != nil {
				return err
			}
		}
	}
	_, err = tx.Exec(`CREATE INDEX IF NOT EXISTS image_content_id ON images(content_id) WHERE content_id IS NOT NULL;
 CREATE INDEX IF NOT EXISTS image_file_hash ON images(file_hash) WHERE file_hash IS NOT NULL;
 CREATE INDEX IF NOT EXISTS image_hash_pending ON images(id,hash_retry_at) WHERE hash_signature IS NULL;
 CREATE TRIGGER IF NOT EXISTS image_hash_invalidate AFTER UPDATE OF signature ON images WHEN old.signature!=new.signature BEGIN
 UPDATE images SET content_id=NULL,file_hash=NULL,hash_signature=NULL,hash_error=NULL,hash_retry_at=0 WHERE id=new.id;
 END;
 CREATE TABLE IF NOT EXISTS phone_backup_hashes(path TEXT PRIMARY KEY,signature TEXT NOT NULL,content_id TEXT,file_hash TEXT NOT NULL,hash_error TEXT);`)
	if err != nil {
		return err
	}
	return tx.Commit()
}

type identityJob struct{ ID, Path, Signature string }

func (l *Library) hashIdentity(ctx context.Context, j identityJob) error {
	// The archive importer has already streamed these hashes while copying. Only
	// reuse them for the exact archived size/mtime signature.
	var cached originalHashes
	var cachedError string
	err := l.db.QueryRowContext(ctx, `SELECT coalesce(content_id,''),file_hash,coalesce(hash_error,'') FROM phone_backup_hashes WHERE path=? AND signature=?`, j.Path, j.Signature).Scan(&cached.ContentID, &cached.FileHash, &cachedError)
	if err == nil {
		return l.saveIdentity(ctx, j, cached, cachedError)
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return err
	}
	f, err := l.open(j.Path)
	if err != nil {
		return err
	}
	defer f.Close()
	before, err := f.Stat()
	if err != nil {
		return err
	}
	if fmt.Sprintf("go1:%d:%d", before.Size(), before.ModTime().UnixNano()) != j.Signature {
		return fmt.Errorf("image changed before hashing")
	}
	hashes, _, hashErr := hashOriginal(ctx, &identityBackgroundReader{ctx: ctx, lib: l, source: f}, j.Path, nil)
	if ctx.Err() != nil {
		return ctx.Err()
	}
	if hashErr != nil && hashes.FileHash == "" {
		return hashErr
	}
	after, err := f.Stat()
	if err != nil {
		return err
	}
	current, err := l.root.Stat(j.Path)
	if err != nil {
		return err
	}
	if !sameBackupFile(before, after) || !sameBackupFile(before, current) {
		return fmt.Errorf("image changed while hashing")
	}
	message := ""
	if hashErr != nil {
		message = hashErr.Error()
	}
	return l.saveIdentity(ctx, j, hashes, message)
}
func (l *Library) saveIdentity(ctx context.Context, j identityJob, h originalHashes, message string) error {
	_, err := l.db.ExecContext(ctx, `UPDATE images SET content_id=nullif(?,''),file_hash=nullif(?,''),hash_signature=?,hash_error=nullif(?,''),hash_retry_at=0 WHERE id=? AND path=? AND signature=?`, h.ContentID, h.FileHash, j.Signature, message, j.ID, j.Path, j.Signature)
	return err
}

func (l *Library) backfillImageIdentities(ctx context.Context) {
	if !waitContext(ctx, 5*time.Second) {
		return
	}
	for ctx.Err() == nil {
		n, err := l.hashIdentityBatch(ctx)
		if err != nil && ctx.Err() == nil {
			log.Printf("Image identity backfill: %v", err)
		}
		delay := time.Second
		if n == 0 || err != nil {
			delay = 30 * time.Second
		}
		if !waitContext(ctx, delay) {
			return
		}
	}
}
func (l *Library) hashIdentityBatch(ctx context.Context) (int, error) {
	rows, err := l.db.QueryContext(ctx, `SELECT id,path,signature FROM images WHERE hash_signature IS NULL AND hash_retry_at<=? ORDER BY id LIMIT 16`, time.Now().Unix())
	if err != nil {
		return 0, err
	}
	var jobs []identityJob
	for rows.Next() {
		var j identityJob
		if err = rows.Scan(&j.ID, &j.Path, &j.Signature); err != nil {
			break
		}
		jobs = append(jobs, j)
	}
	rowErr := rows.Err()
	rows.Close()
	if err != nil {
		return 0, err
	}
	if rowErr != nil {
		return 0, rowErr
	}
	for _, j := range jobs {
		for len(l.renderSlots) > 0 || time.Since(time.Unix(0, l.lastRender.Load())) < 200*time.Millisecond {
			if !waitContext(ctx, 100*time.Millisecond) {
				return 0, ctx.Err()
			}
		}
		if err = l.hashIdentity(ctx, j); err != nil {
			if ctx.Err() != nil {
				return 0, ctx.Err()
			}
			// Failed files must not starve later entries, including across restarts.
			if _, e := l.db.ExecContext(ctx, `UPDATE images SET hash_error=?,hash_retry_at=? WHERE id=? AND signature=? AND hash_signature IS NULL`, err.Error(), time.Now().Add(time.Hour).Unix(), j.ID, j.Signature); e != nil {
				return 0, e
			}
		}
		if !waitContext(ctx, 100*time.Millisecond) {
			return 0, ctx.Err()
		}
	}
	return len(jobs), nil
}

func (l *Library) imageIdentity(w http.ResponseWriter, r *http.Request) error {
	id := r.PathValue("id")
	clause, args, err := l.curationSelection(r, "id=?", []any{id})
	if err != nil {
		return err
	}
	var content, file, signature, current, message sql.NullString
	err = l.read.QueryRowContext(r.Context(), `SELECT content_id,file_hash,hash_signature,signature,hash_error FROM images WHERE `+clause, args...).Scan(&content, &file, &signature, &current, &message)
	if errors.Is(err, sql.ErrNoRows) {
		return bad(404, "Unknown image")
	}
	if err != nil {
		return err
	}
	matches := []map[string]string{}
	if signature.Valid && signature.String == current.String && content.Valid {
		clause, args, err = l.curationSelection(r, "content_id=? AND id!=? AND hash_signature=signature", []any{content.String, id})
		if err != nil {
			return err
		}
		rows, e := l.read.QueryContext(r.Context(), `SELECT id,path FROM images WHERE `+clause+` ORDER BY id LIMIT 100`, args...)
		if e != nil {
			return e
		}
		defer rows.Close()
		for rows.Next() {
			var matchID, path string
			if e = rows.Scan(&matchID, &path); e != nil {
				return e
			}
			matches = append(matches, map[string]string{"id": matchID, "path": path})
		}
		if e = rows.Err(); e != nil {
			return e
		}
	}
	return writeJSON(w, 200, map[string]any{"image_id": id, "content_id": nullableIdentity(content), "file_hash": nullableIdentity(file), "pending": !signature.Valid || signature.String != current.String, "error": nullableIdentity(message), "potential_duplicates": matches, "matches_limit": 100})
}
func nullableIdentity(s sql.NullString) any {
	if s.Valid {
		return s.String
	}
	return nil
}

// Throttle within large originals too; one worker and bounded buffers keep the
// backfill from monopolizing disk bandwidth or delaying foreground requests.
type identityBackgroundReader struct {
	ctx    context.Context
	lib    *Library
	source io.Reader
	bytes  int
}

func (r *identityBackgroundReader) Read(p []byte) (int, error) {
	for len(r.lib.renderSlots) > 0 || time.Since(time.Unix(0, r.lib.lastRender.Load())) < 200*time.Millisecond {
		if !waitContext(r.ctx, 50*time.Millisecond) {
			return 0, r.ctx.Err()
		}
	}
	if r.bytes >= 4<<20 {
		r.bytes = 0
		if !waitContext(r.ctx, 20*time.Millisecond) {
			return 0, r.ctx.Err()
		}
	}
	n, e := r.source.Read(p)
	r.bytes += n
	return n, e
}
