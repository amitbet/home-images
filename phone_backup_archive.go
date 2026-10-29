package main

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"
	"unicode"
)

func backupName(name string) string {
	var out []rune
	for _, r := range name {
		if unicode.IsLetter(r) || unicode.IsDigit(r) || r == '-' || r == '_' {
			out = append(out, r)
		} else {
			out = append(out, '-')
		}
		if len(out) >= 40 {
			break
		}
	}
	s := strings.Trim(string(out), "-.")
	if s == "" {
		return "Phone"
	}
	return s
}
func backupMedia(path string) bool {
	if supported(path) {
		return true
	}
	switch strings.ToLower(filepath.Ext(path)) {
	case ".mp4", ".mov", ".m4v", ".3gp", ".avi", ".mkv", ".webm", ".dng", ".raw", ".aae", ".xmp":
		return true
	}
	return false
}

func (b *phoneBackup) prepareArchive() error {
	if err := os.MkdirAll(b.base, 0700); err != nil {
		return err
	}
	base, err := filepath.EvalSymlinks(b.base)
	if err != nil {
		return err
	}
	inside := func(parent, child string) bool {
		rel, err := filepath.Rel(parent, child)
		return err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator))
	}
	if inside(b.lib.rootPath, base) || inside(base, b.lib.rootPath) {
		return fmt.Errorf("phone backup requires data_dir/syncthing and the photo library to be separate directories")
	}
	if err = b.lib.root.MkdirAll("Phone backups", 0750); err != nil {
		return fmt.Errorf("create Phone backups folder: %w; grant the Home Images service write access to that folder or rerun the installer", err)
	}
	archive, err := b.lib.root.OpenRoot("Phone backups")
	if err != nil {
		return err
	}
	defer archive.Close()
	name := ".write-" + rand.Text() + ".tmp"
	f, err := archive.OpenFile(name, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if err != nil {
		return fmt.Errorf("Phone backups is not writable by the Home Images service: %w; rerun the installer to grant access", err)
	}
	f.Close()
	return archive.Remove(name)
}

func (b *phoneBackup) importFolders(ctx context.Context) error {
	b.mu.Lock()
	// Copy nested slices: API mutations always replace, never edit these slices.
	phones := append([]backupPhone(nil), b.settings.Phones...)
	b.mu.Unlock()
	changed := false
	for _, phone := range phones {
		for _, folder := range phone.Folders {
			var status struct {
				State          string
				Sequence       int64
				NeedTotalItems int
				PullErrors     int
			}
			if err := b.call(ctx, "GET", "/rest/db/status?folder="+url.QueryEscape(folder.ID), nil, &status); err != nil {
				return err
			}
			// Syncthing writes temporary files and renames them when complete. Wait
			// until the folder is idle with no outstanding downloads before import.
			if status.State != "idle" || status.NeedTotalItems != 0 || status.PullErrors != 0 {
				continue
			}
			key := phone.ID + "/" + folder.ID
			if sequence, ok := b.importedSequences[key]; ok && sequence == status.Sequence {
				continue
			}
			n, err := b.importFolder(ctx, phone, folder)
			changed = changed || n > 0
			if err != nil {
				return err
			}
			if b.importedSequences == nil {
				b.importedSequences = make(map[string]int64)
			}
			b.importedSequences[key] = status.Sequence
		}
	}
	if changed {
		b.lib.Scan(ctx)
	}
	return nil
}

func (b *phoneBackup) importFolder(ctx context.Context, phone backupPhone, folder backupFolder) (int, error) {
	source, err := os.OpenRoot(filepath.Join(b.base, "incoming", folder.Directory))
	if err != nil {
		return 0, err
	}
	defer source.Close()
	if err = b.lib.root.MkdirAll("Phone backups", 0750); err != nil {
		return 0, err
	}
	archive, err := b.lib.root.OpenRoot("Phone backups")
	if err != nil {
		return 0, err
	}
	defer archive.Close()
	count := 0
	err = fs.WalkDir(source.FS(), ".", func(path string, d fs.DirEntry, walkErr error) error {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if walkErr != nil {
			return walkErr
		}
		if d.Type()&os.ModeSymlink != 0 {
			return nil
		}
		if strings.HasPrefix(d.Name(), ".") {
			if d.IsDir() && path != "." {
				return fs.SkipDir
			}
			if !d.IsDir() {
				return nil
			}
		}
		if d.IsDir() || !backupMedia(path) {
			return nil
		}
		info, err := d.Info()
		if err != nil {
			return err
		}
		if !info.Mode().IsRegular() {
			return nil
		}
		key := phone.ID + "/" + folder.ID + "/" + path
		signature := fmt.Sprintf("%d:%d", info.Size(), info.ModTime().UnixNano())
		var previous string
		err = b.lib.db.QueryRowContext(ctx, "SELECT signature FROM phone_backup_files WHERE source=?", key).Scan(&previous)
		if err != nil && !errors.Is(err, sql.ErrNoRows) {
			return err
		}
		if previous == signature {
			return nil
		}
		phoneDir := phone.Directory
		if phoneDir == "" {
			phoneDir = folderKey(phone.ID)
		}
		dir := filepath.Join(phoneDir, folderKey(folder.ID), filepath.Dir(filepath.FromSlash(path)))
		if err = archive.MkdirAll(dir, 0750); err != nil {
			return err
		}
		f, err := source.Open(path)
		if err != nil {
			return err
		}
		defer f.Close()
		// A final file can be replaced during a scan. Check the opened file and
		// its current path both before and after copying to avoid archiving a mix.
		before, err := f.Stat()
		if err != nil {
			return err
		}
		if !sameBackupFile(info, before) {
			return nil
		}
		temp := filepath.Join(dir, ".backup-"+rand.Text()+".tmp")
		out, err := archive.OpenFile(temp, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0640)
		if err != nil {
			return err
		}
		defer archive.Remove(temp)
		hashes, n, copyErr := hashOriginal(ctx, f, path, out)
		hashError := ""
		if copyErr != nil {
			hashError = copyErr.Error()
		}
		// A metadata parser failure must not prevent archiving a completed file.
		// The checksum is still valid if the entire copy completed.
		if copyErr != nil && hashes.FileHash != "" {
			copyErr = nil
		}
		if copyErr == nil {
			copyErr = out.Sync()
		}
		if closeErr := out.Close(); copyErr == nil {
			copyErr = closeErr
		}
		if copyErr != nil {
			return copyErr
		}
		after, err := f.Stat()
		if err != nil {
			return err
		}
		current, err := source.Stat(path)
		if errors.Is(err, os.ErrNotExist) {
			return nil
		}
		if err != nil {
			return err
		}
		if n != before.Size() || !sameBackupFile(before, after) || !sameBackupFile(before, current) {
			return nil
		}
		stem := strings.TrimSuffix(d.Name(), filepath.Ext(d.Name()))
		runes := []rune(stem)
		if len(runes) > 60 {
			stem = string(runes[:60])
		}
		name := stem + "--" + strings.TrimPrefix(hashes.FileHash, "sha256:") + filepath.Ext(d.Name())
		destination := filepath.Join(dir, name)
		if err = archive.Chtimes(temp, time.Now(), before.ModTime()); err != nil {
			return err
		}
		// Link publishes atomically with no replacement. Existing archives are
		// immutable; retries and identical content never overwrite originals.
		if err = archive.Link(temp, destination); err != nil {
			if !errors.Is(err, os.ErrExist) {
				return err
			}
			existing, openErr := archive.Open(destination)
			if openErr != nil {
				return openErr
			}
			existingHash := sha256.New()
			_, copyErr := io.Copy(existingHash, &backupContextReader{ctx: ctx, r: existing})
			existing.Close()
			if copyErr != nil {
				return copyErr
			}
			if fmt.Sprintf("sha256:%x", existingHash.Sum(nil)) != hashes.FileHash {
				return fmt.Errorf("archive integrity check failed for %s; existing file was preserved", destination)
			}
		}
		archivePath := filepath.ToSlash(filepath.Join("Phone backups", destination))
		if _, err = b.lib.db.ExecContext(ctx, `INSERT INTO phone_backup_hashes(path,signature,content_id,file_hash,hash_error) VALUES(?,?,nullif(?,''),?,nullif(?,'')) ON CONFLICT(path) DO UPDATE SET signature=excluded.signature,content_id=excluded.content_id,file_hash=excluded.file_hash,hash_error=excluded.hash_error`, archivePath, fmt.Sprintf("go1:%d:%d", before.Size(), before.ModTime().UnixNano()), hashes.ContentID, hashes.FileHash, hashError); err != nil {
			return err
		}
		_, err = b.lib.db.ExecContext(ctx, `INSERT INTO phone_backup_files(source,signature,archive,imported_at) VALUES(?,?,?,?) ON CONFLICT(source) DO UPDATE SET signature=excluded.signature,archive=excluded.archive,imported_at=excluded.imported_at`, key, signature, filepath.ToSlash(filepath.Join("Phone backups", destination)), time.Now().UTC().Format(time.RFC3339))
		if err == nil {
			count++
		}
		return err
	})
	return count, err
}
func sameBackupFile(a, b fs.FileInfo) bool {
	return a.Mode().IsRegular() && b.Mode().IsRegular() && a.Size() == b.Size() && a.ModTime().Equal(b.ModTime()) && os.SameFile(a, b)
}

type backupContextReader struct {
	ctx context.Context
	r   io.Reader
}

func (r *backupContextReader) Read(p []byte) (int, error) {
	if err := r.ctx.Err(); err != nil {
		return 0, err
	}
	return r.r.Read(p)
}
