package main

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"io/fs"
	"log"
	"os"
	"path/filepath"
	"strings"
)

// Reconcile a notification only after a brief quiet period. Never remove catalog
// entries on transient read errors or when the root is unavailable.
func (l *Library) reconcileChangedPath(ctx context.Context, path string) error {
	path = filepath.ToSlash(path)
	if path == ".homeimages-quarantine" || strings.HasPrefix(path, ".homeimages-quarantine/") {
		return nil
	}
	name, err := cleanRelative(path)
	if err != nil || name == "." {
		return fmt.Errorf("invalid notification path %q", path)
	}
	l.scanMu.Lock()
	defer l.scanMu.Unlock()
	info, err := l.root.Lstat(name)
	if errors.Is(err, os.ErrNotExist) {
		root, e := l.root.Stat(".")
		if e != nil || !root.IsDir() {
			return fmt.Errorf("root unavailable")
		}
		return l.retireTree(ctx, path)
	}
	if err != nil {
		return err
	}
	if info.Mode()&os.ModeSymlink != 0 {
		return nil
	}
	if info.IsDir() {
		return fs.WalkDir(l.root.FS(), filepath.ToSlash(name), func(p string, d fs.DirEntry, e error) error {
			if e != nil {
				return e
			}
			if ctx.Err() != nil {
				return ctx.Err()
			}
			if d.Type()&os.ModeSymlink != 0 {
				if d.IsDir() {
					return fs.SkipDir
				}
				return nil
			}
			if d.IsDir() || !supported(p) {
				return nil
			}
			return l.indexChangedFile(ctx, p)
		})
	}
	if !supported(path) {
		return nil
	}
	return l.indexChangedFile(ctx, path)
}

func (l *Library) indexChangedFile(ctx context.Context, path string) error {
	f, err := l.open(path)
	if err != nil {
		return err
	}
	info, err := f.Stat()
	f.Close()
	if err != nil {
		return err
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
		return err
	}
	err = l.storePhoto(ctx, photo, signature)
	if err == nil {
		log.Printf("Filesystem watcher indexed %s", path)
	}
	return err
}

func validWatchPath(p string) bool { return p != "" && !strings.HasPrefix(p, "/") }
