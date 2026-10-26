package main

import (
	"context"
	"fmt"
	"net/url"
	"path/filepath"
	"runtime"
	"strings"
)

// ImportLocations imports derived labels only; originals and manual annotations
// are untouched. The caller must stop the service before opening its catalog.
func (l *Library) ImportLocations(ctx context.Context, source string) (int64, error) {
	absolute, err := filepath.Abs(source)
	if err != nil {
		return 0, err
	}
	path := filepath.ToSlash(absolute)
	if !strings.HasPrefix(path, "/") {
		path = "/" + path
	}
	uri := (&url.URL{Scheme: "file", Path: path, RawQuery: "mode=ro"}).String()
	conn, err := l.db.Conn(ctx)
	if err != nil {
		return 0, err
	}
	defer conn.Close()
	if _, err = conn.ExecContext(ctx, "ATTACH DATABASE ? AS location_import", uri); err != nil {
		return 0, err
	}
	defer conn.ExecContext(context.Background(), "DETACH DATABASE location_import")
	var root string
	if err = conn.QueryRowContext(ctx, "SELECT value FROM location_import.settings WHERE key='root'").Scan(&root); err != nil {
		return 0, err
	}
	root, err = filepath.EvalSymlinks(root)
	if err != nil {
		return 0, err
	}
	root, err = filepath.Abs(root)
	if err != nil {
		return 0, err
	}
	matches := root == l.rootPath
	if runtime.GOOS == "windows" {
		matches = strings.EqualFold(root, l.rootPath)
	}
	if !matches {
		return 0, fmt.Errorf("location package belongs to a different photo root")
	}
	tx, err := conn.BeginTx(ctx, nil)
	if err != nil {
		return 0, err
	}
	defer tx.Rollback()
	result, err := tx.ExecContext(ctx, `INSERT INTO main.locations(image_id,signature,label)
 SELECT g.image_id,g.signature,g.label FROM location_import.locations g
 JOIN main.images i ON i.id=g.image_id AND i.signature=g.signature
 WHERE true
 ON CONFLICT(image_id) DO UPDATE SET signature=excluded.signature,label=excluded.label`)
	if err != nil {
		return 0, err
	}
	count, err := result.RowsAffected()
	if err != nil {
		return 0, err
	}
	if err = tx.Commit(); err != nil {
		return 0, err
	}
	return count, nil
}
