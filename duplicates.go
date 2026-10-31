package main

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"time"
)

type duplicateFile struct {
	ID   string `json:"id"`
	Path string `json:"path"`
	Hash string `json:"file_hash"`
	Size int64  `json:"size"`
}

func (l *Library) duplicateGroups(w http.ResponseWriter, r *http.Request) error {
	limit, err := intParam(r, "limit", 20, 1, 100)
	if err != nil {
		return err
	}
	offset, err := intParam(r, "offset", 0, 0, 1000000)
	if err != nil {
		return err
	}
	var total int
	if err = l.read.QueryRowContext(r.Context(), `SELECT count(*) FROM (SELECT f.asset_id FROM photo_files f JOIN images i ON i.id=f.file_id GROUP BY f.asset_id HAVING count(*)>1)`).Scan(&total); err != nil {
		return err
	}
	rows, err := l.read.QueryContext(r.Context(), `WITH groups AS (SELECT f.asset_id asset FROM photo_files f JOIN images i ON i.id=f.file_id GROUP BY f.asset_id HAVING count(*)>1 ORDER BY f.asset_id LIMIT ? OFFSET ?) SELECT i.id,i.path,i.file_hash,i.size_bytes,f.asset_id FROM groups g JOIN photo_files f ON f.asset_id=g.asset JOIN images i ON i.id=f.file_id ORDER BY g.asset,i.path`, limit, offset)
	if err != nil {
		return err
	}
	defer rows.Close()
	type group struct {
		Asset string          `json:"asset"`
		Files []duplicateFile `json:"files"`
	}
	groups := []group{}
	for rows.Next() {
		var f duplicateFile
		var asset string
		if err = rows.Scan(&f.ID, &f.Path, &f.Hash, &f.Size, &asset); err != nil {
			return err
		}
		if len(groups) == 0 || groups[len(groups)-1].Asset != asset {
			groups = append(groups, group{Asset: asset, Files: []duplicateFile{}})
		}
		groups[len(groups)-1].Files = append(groups[len(groups)-1].Files, f)
	}
	if err = rows.Err(); err != nil {
		return err
	}
	return writeJSON(w, 200, map[string]any{"items": groups, "total": total, "offset": offset})
}
func (l *Library) liveFileHash(r *http.Request, id string) (Photo, string, error) {
	p, err := l.get(r.Context(), id)
	if err == nil && p.ID != id {
		return p, "", bad(409, "This file location is no longer current; refresh the duplicate list")
	}
	if err != nil {
		return p, "", err
	}
	f, err := l.open(p.Path)
	if err != nil {
		return p, "", err
	}
	defer f.Close()
	before, err := f.Stat()
	if err != nil {
		return p, "", err
	}
	h, _, e := hashOriginal(r.Context(), f, p.Path, nil)
	if e != nil && h.FileHash == "" {
		return p, "", e
	}
	after, err := f.Stat()
	if err != nil {
		return p, "", err
	}
	current, err := l.root.Stat(filepath.FromSlash(p.Path))
	if err != nil || !sameBackupFile(before, after) || !sameBackupFile(before, current) {
		return p, "", bad(409, "File changed during verification; refresh and retry")
	}
	return p, h.FileHash, nil
}
func (l *Library) quarantineDuplicate(w http.ResponseWriter, r *http.Request) error {
	var body struct {
		Keep   string `json:"keep"`
		Remove string `json:"remove"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		return bad(400, "Invalid request")
	}
	result, err := l.quarantinePair(r, body.Keep, body.Remove)
	if err != nil {
		return err
	}
	return writeJSON(w, 200, result)
}
func (l *Library) quarantinePair(r *http.Request, keepID, removeID string) (map[string]any, error) {
	return l.quarantinePairExpected(r, keepID, removeID, "")
}
func (l *Library) quarantinePairExpected(r *http.Request, keepID, removeID, expected string, expectedPaths ...string) (map[string]any, error) {
	if keepID == removeID || keepID == "" || removeID == "" {
		return nil, bad(400, "Choose a copy to keep and a different copy to move to Trash")
	}
	l.scanMu.Lock()
	defer l.scanMu.Unlock()
	keep, kh, err := l.liveFileHash(r, keepID)
	if err != nil {
		return nil, err
	}
	remove, rh, err := l.liveFileHash(r, removeID)
	if err != nil {
		return nil, err
	}
	if len(expectedPaths) == 2 && (keep.Path != expectedPaths[0] || remove.Path != expectedPaths[1]) {
		return nil, bad(409, "File paths changed after review")
	}
	if kh != rh || (expected != "" && kh != expected) {
		return nil, bad(409, "These files are not exact duplicates. Their metadata or image data differs.")
	}
	raw := make([]byte, 16)
	if _, err = rand.Read(raw); err != nil {
		return nil, err
	}
	token := hex.EncodeToString(raw)
	dir := ".homeimages-quarantine/" + token
	if err = l.root.MkdirAll(filepath.FromSlash(dir), 0750); err != nil {
		return nil, fmt.Errorf("cannot create Trash (service needs write access): %w", err)
	}
	dest := dir + "/" + remove.Filename
	// Write the recovery record before moving the file; it survives a crash.
	_, err = l.db.ExecContext(r.Context(), "INSERT INTO duplicate_quarantine(token,image_id,path,quarantine_path,created_at,keep_id,file_hash) VALUES(?,?,?,?,?,?,?)", token, remove.ID, remove.Path, dest, time.Now().UTC().Format(time.RFC3339), keep.ID, kh)
	if err != nil {
		return nil, err
	}
	if err = l.root.Rename(filepath.FromSlash(remove.Path), filepath.FromSlash(dest)); err != nil {
		return nil, fmt.Errorf("cannot move file to Trash (service needs permission to move this copy): %w", err)
	}
	qf, e := l.root.Open(filepath.FromSlash(dest))
	if e != nil {
		_ = l.root.Rename(filepath.FromSlash(dest), filepath.FromSlash(remove.Path))
		return nil, e
	}
	qh, _, e := hashOriginal(r.Context(), qf, remove.Path, nil)
	qf.Close()
	if (e != nil && qh.FileHash == "") || qh.FileHash != rh {
		_ = l.root.Rename(filepath.FromSlash(dest), filepath.FromSlash(remove.Path))
		return nil, bad(409, "Selected copy changed; cleanup cancelled")
	}
	// Verify the retained copy is still available before changing the catalog.
	if _, h, e := l.liveFileHash(r, keep.ID); e != nil || h != kh {
		_ = l.root.Rename(filepath.FromSlash(dest), filepath.FromSlash(remove.Path))
		return nil, bad(409, "Retained copy changed; cleanup cancelled")
	}
	if err = l.preserveDuplicateCuration(r, keep.ID, remove.ID); err != nil {
		_ = l.root.Rename(filepath.FromSlash(dest), filepath.FromSlash(remove.Path))
		return nil, err
	}
	if err = l.retirePath(r.Context(), remove.Path); err != nil {
		_ = l.root.Rename(filepath.FromSlash(dest), filepath.FromSlash(remove.Path))
		return nil, err
	}
	return map[string]any{"ok": true, "token": token, "kept": keep.Path, "quarantined": remove.Path}, nil
}
func (l *Library) quarantineList(w http.ResponseWriter, r *http.Request) error {
	query := "SELECT token,path,quarantine_path,created_at FROM duplicate_quarantine q"
	if r.URL.Query().Get("ungrouped") == "1" {
		query += " WHERE NOT EXISTS (SELECT 1 FROM duplicate_batch_items i JOIN duplicate_batches b ON b.id=i.batch_id AND b.action='quarantine' WHERE i.token=q.token AND i.state='done')"
	}
	rows, err := l.read.QueryContext(r.Context(), query+" ORDER BY created_at DESC")
	if err != nil {
		return err
	}
	defer rows.Close()
	items := []map[string]string{}
	for rows.Next() {
		var token, path, dest, created string
		if err = rows.Scan(&token, &path, &dest, &created); err != nil {
			return err
		}
		if _, e := l.root.Stat(filepath.FromSlash(dest)); e == nil {
			items = append(items, map[string]string{"token": token, "path": path, "created": created})
		}
	}
	if err = rows.Err(); err != nil {
		return err
	}
	return writeJSON(w, 200, map[string]any{"items": items})
}
func (l *Library) restoreDuplicate(w http.ResponseWriter, r *http.Request) error {
	var body struct {
		Token string `json:"token"`
	}
	if json.NewDecoder(r.Body).Decode(&body) != nil {
		return bad(400, "Invalid request")
	}
	l.scanMu.Lock()
	defer l.scanMu.Unlock()
	var path, dest string
	if err := l.db.QueryRowContext(r.Context(), "SELECT path,quarantine_path FROM duplicate_quarantine WHERE token=?", body.Token).Scan(&path, &dest); err != nil {
		return bad(404, "Copy not found in Trash")
	}
	if _, err := l.root.Lstat(filepath.FromSlash(path)); !os.IsNotExist(err) {
		return bad(409, "Original path is occupied or inaccessible")
	}
	if err := l.root.MkdirAll(filepath.Dir(filepath.FromSlash(path)), 0750); err != nil {
		return err
	}
	// Link creation fails atomically if the original path becomes occupied.
	// Both paths are on the library volume; unlink quarantine only after success.
	if err := l.root.Link(filepath.FromSlash(dest), filepath.FromSlash(path)); err != nil {
		return fmt.Errorf("cannot restore without replacing an existing file: %w", err)
	}
	if err := l.root.Remove(filepath.FromSlash(dest)); err != nil {
		return err
	}
	photo, err := l.metadata(r.Context(), path)
	if err != nil {
		return err
	}
	info, err := l.root.Stat(filepath.FromSlash(path))
	if err != nil {
		return err
	}
	if err = l.storePhoto(r.Context(), photo, fmt.Sprintf("go1:%d:%d", info.Size(), info.ModTime().UnixNano())); err != nil {
		return err
	}
	_, err = l.db.ExecContext(r.Context(), "DELETE FROM duplicate_quarantine WHERE token=?", body.Token)
	if err != nil {
		return err
	}
	return writeJSON(w, 200, map[string]any{"ok": true})
}

func (l *Library) purgeDuplicate(w http.ResponseWriter, r *http.Request) error {
	var body struct {
		Token string `json:"token"`
	}
	if json.NewDecoder(r.Body).Decode(&body) != nil {
		return bad(400, "Invalid request")
	}
	l.scanMu.Lock()
	defer l.scanMu.Unlock()
	var dest, keep, expected string
	if err := l.db.QueryRowContext(r.Context(), "SELECT quarantine_path,coalesce(keep_id,''),coalesce(file_hash,'') FROM duplicate_quarantine WHERE token=?", body.Token).Scan(&dest, &keep, &expected); err != nil {
		return bad(404, "Copy not found in Trash")
	}
	if expected == "" || keep == "" {
		return bad(409, "This recovery record cannot authorize permanent deletion")
	}
	if _, hash, err := l.liveFileHash(r, keep); err != nil || hash != expected {
		return bad(409, "Retained original is missing or changed; permanent deletion refused")
	}
	f, err := l.root.Open(filepath.FromSlash(dest))
	if err != nil {
		return err
	}
	h, _, err := hashOriginal(r.Context(), f, dest, nil)
	f.Close()
	if (err != nil && h.FileHash == "") || h.FileHash != expected {
		return bad(409, "Copy in Trash changed; permanent deletion refused")
	}
	if err = l.root.Remove(filepath.FromSlash(dest)); err != nil {
		return err
	}
	_, err = l.db.ExecContext(r.Context(), "DELETE FROM duplicate_quarantine WHERE token=?", body.Token)
	if err != nil {
		return err
	}
	return writeJSON(w, 200, map[string]any{"ok": true})
}

func (l *Library) preserveDuplicateCuration(r *http.Request, keep, remove string) error {
	tx, err := l.db.BeginTx(r.Context(), nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	_, err = tx.ExecContext(r.Context(), `INSERT OR IGNORE INTO curation.annotations(root,image_id,favorite,hidden,title,description,location,tags,updated_at) SELECT root,?,favorite,hidden,title,description,location,tags,updated_at FROM curation.annotations WHERE root=? AND image_id=?`, keep, l.rootPath, remove)
	if err != nil {
		return err
	}
	_, err = tx.ExecContext(r.Context(), `UPDATE curation.annotations SET favorite=max(favorite,coalesce((SELECT favorite FROM curation.annotations WHERE root=? AND image_id=?),0)),tags=(SELECT json_group_array(value) FROM (SELECT value FROM json_each(curation.annotations.tags) UNION SELECT j.value FROM curation.annotations source,json_each(source.tags) j WHERE source.root=? AND source.image_id=?)) WHERE root=? AND image_id=?`, l.rootPath, remove, l.rootPath, remove, l.rootPath, keep)
	if err != nil {
		return err
	}
	_, err = tx.ExecContext(r.Context(), `INSERT OR IGNORE INTO curation.album_images(album_id,image_id) SELECT m.album_id,? FROM curation.album_images m JOIN curation.albums a ON a.id=m.album_id WHERE a.root=? AND m.image_id=?`, keep, l.rootPath, remove)
	if err != nil {
		return err
	}
	return tx.Commit()
}
