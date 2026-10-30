package main

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
)

// File rows retain legacy IDs; content-addressed assets join their locations.
// Only a verified exact-byte match may reconnect a missing location automatically.
func (l *Library) storePhoto(ctx context.Context, photo Photo, signature string) error {
	var existing string
	err := l.db.QueryRowContext(ctx, "SELECT id FROM images WHERE path=?", photo.Path).Scan(&existing)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return err
	}
	var hashes originalHashes
	reconnected := false
	if existing != "" {
		photo.ID = existing
	} else if l.hasMissingIdentityCandidate(ctx, photo.Path, photo.Size) {
		f, e := l.open(photo.Path)
		if e != nil {
			return e
		}
		before, e := f.Stat()
		if e != nil {
			f.Close()
			return e
		}
		hashes, _, e = hashOriginal(ctx, f, photo.Path, nil)
		after, statErr := f.Stat()
		f.Close()
		if statErr != nil {
			return statErr
		}
		if !sameBackupFile(before, after) || fmt.Sprintf("go1:%d:%d", after.Size(), after.ModTime().UnixNano()) != signature {
			return fmt.Errorf("file changed while identifying %s", photo.Path)
		}
		if e != nil && hashes.FileHash == "" {
			return e
		}
		rows, e := l.db.QueryContext(ctx, `SELECT id,path FROM images WHERE file_hash=? AND hash_signature=signature UNION SELECT id,path FROM image_tombstones WHERE file_hash=?`, hashes.FileHash, hashes.FileHash)
		if e != nil {
			return e
		}
		candidates := map[string]string{}
		for rows.Next() {
			var id, path string
			if e = rows.Scan(&id, &path); e != nil {
				break
			}
			_, statErr := l.root.Lstat(filepath.FromSlash(path))
			if path == photo.Path || errors.Is(statErr, os.ErrNotExist) {
				candidates[id] = path
			}
		}
		rowErr := rows.Err()
		rows.Close()
		if e != nil {
			return e
		}
		if rowErr != nil {
			return rowErr
		}
		// Folder renames commonly move several exact copies at once. A unique
		// matching basename disambiguates their per-copy saved information.
		if len(candidates) > 1 {
			var chosen string
			count := 0
			for id, path := range candidates {
				if filepath.Base(filepath.FromSlash(path)) == photo.Filename {
					chosen = id
					count++
				}
			}
			if count == 1 {
				candidates = map[string]string{chosen: candidates[chosen]}
			}
		}
		if len(candidates) == 1 {
			for id := range candidates {
				photo.ID = id
				reconnected = true
			}
		}
	}
	if existing == "" && !reconnected {
		var occupied int
		if err = l.db.QueryRowContext(ctx, "SELECT EXISTS(SELECT 1 FROM images WHERE id=?) OR EXISTS(SELECT 1 FROM image_tombstones WHERE id=?)", photo.ID, photo.ID).Scan(&occupied); err != nil {
			return err
		}
		if occupied != 0 {
			raw := make([]byte, 12)
			if _, err = rand.Read(raw); err != nil {
				return err
			}
			photo.ID = hex.EncodeToString(raw)
		}
	}
	photo.ImageURL = "/api/images/" + photo.ID + "/image"
	photo.OriginalURL = "/api/images/" + photo.ID + "/original"
	encoded, err := json.Marshal(photo)
	if err != nil {
		return err
	}
	// UPDATE retains hashes, annotations, and identity for an unchanged signature.
	database := l.db
	if reconnected && l.ai != nil {
		database = l.ai.db
	}
	tx, err := database.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if reconnected && l.ai != nil {
		var oldSignature string
		if err = tx.QueryRowContext(ctx, "SELECT signature FROM catalog.images WHERE id=? UNION ALL SELECT signature FROM catalog.image_tombstones WHERE id=? LIMIT 1", photo.ID, photo.ID).Scan(&oldSignature); err != nil {
			return err
		}
		if _, err = tx.ExecContext(ctx, "UPDATE ai_images SET signature=? WHERE image_id=? AND signature=?", signature, photo.ID, oldSignature); err != nil {
			return err
		}
		if _, err = tx.ExecContext(ctx, "UPDATE face_overrides SET signature=? WHERE image_id=? AND signature=?", signature, photo.ID, oldSignature); err != nil {
			return err
		}
		if _, err = tx.ExecContext(ctx, "UPDATE catalog.locations SET signature=? WHERE image_id=? AND signature=?", signature, photo.ID, oldSignature); err != nil {
			return err
		}
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO images(id,path,directory,signature,metadata,search) VALUES(?,?,?,?,metadata_pack(?),metadata_search(?)) ON CONFLICT(id) DO UPDATE SET path=excluded.path,directory=excluded.directory,signature=excluded.signature,metadata=excluded.metadata,search=excluded.search`, photo.ID, photo.Path, photo.Directory, signature, string(encoded), string(encoded))
	if err != nil {
		return err
	}
	_, err = tx.ExecContext(ctx, "DELETE FROM image_tombstones WHERE id=?", photo.ID)
	if err != nil {
		return err
	}
	if err = tx.Commit(); err != nil {
		return err
	}
	if hashes.FileHash != "" {
		return l.saveIdentity(ctx, identityJob{photo.ID, photo.Path, signature}, hashes, "")
	}
	return nil
}
func (l *Library) retirePath(ctx context.Context, path string) error {
	tx, err := l.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	_, err = tx.ExecContext(ctx, `INSERT OR REPLACE INTO image_tombstones SELECT id,path,file_hash,content_id,signature,metadata,size_bytes FROM images WHERE path=?`, path)
	if err != nil {
		return err
	}
	_, err = tx.ExecContext(ctx, "DELETE FROM images WHERE path=?", path)
	if err != nil {
		return err
	}
	return tx.Commit()
}
func (l *Library) retireTree(ctx context.Context, path string) error {
	rows, err := l.db.QueryContext(ctx, `SELECT path FROM images WHERE path=? OR substr(path,1,length(?)+1)=?||'/'`, path, path, path)
	if err != nil {
		return err
	}
	var paths []string
	for rows.Next() {
		var p string
		if err = rows.Scan(&p); err != nil {
			break
		}
		paths = append(paths, p)
	}
	rowErr := rows.Err()
	rows.Close()
	if err != nil {
		return err
	}
	if rowErr != nil {
		return rowErr
	}
	for _, p := range paths {
		if err = l.retirePath(ctx, p); err != nil {
			return err
		}
	}
	return nil
}

// Relocation is explicit and checked against original bytes before accepting a
// different root. The catalog, annotations and face database keep their IDs.
func (l *Library) verifyRootRelocation(ctx context.Context) error {
	rows, err := l.db.QueryContext(ctx, "SELECT path,file_hash FROM images WHERE file_hash IS NOT NULL AND hash_signature=signature ORDER BY path LIMIT 10")
	if err != nil {
		return err
	}
	type sample struct{ path, hash string }
	var samples []sample
	for rows.Next() {
		var s sample
		if err = rows.Scan(&s.path, &s.hash); err != nil {
			break
		}
		samples = append(samples, s)
	}
	rowErr := rows.Err()
	rows.Close()
	if err != nil {
		return err
	}
	if rowErr != nil {
		return rowErr
	}
	if len(samples) == 0 {
		return fmt.Errorf("root relocation needs verified file hashes")
	}
	for _, s := range samples {
		f, e := l.open(s.path)
		if e != nil {
			return fmt.Errorf("root relocation verification: %w", e)
		}
		h, _, e := hashOriginal(ctx, f, s.path, nil)
		f.Close()
		if (e != nil && h.FileHash == "") || h.FileHash != s.hash {
			return fmt.Errorf("new root does not match stored file %s", s.path)
		}
	}
	return nil
}

func (l *Library) hasMissingIdentityCandidate(ctx context.Context, currentPath string, size int64) bool {
	rows, err := l.db.QueryContext(ctx, `SELECT path FROM images WHERE size_bytes=? AND file_hash IS NOT NULL AND hash_signature=signature UNION SELECT path FROM image_tombstones WHERE size_bytes=? AND file_hash IS NOT NULL`, size, size)
	if err != nil {
		return false
	}
	defer rows.Close()
	for rows.Next() {
		var path string
		if rows.Scan(&path) != nil {
			return false
		}
		if _, e := l.root.Lstat(filepath.FromSlash(path)); path == currentPath || errors.Is(e, os.ErrNotExist) {
			return true
		}
	}
	return false
}
