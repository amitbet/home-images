package main

import (
	"context"
	"fmt"
	"net/url"
	"path/filepath"
	"strings"
)

// ImportPython preserves URLs and metadata while the Go scanner refreshes entries.
// SQLite opens the source read-only, including when it has a live WAL writer.
func (l *Library) ImportPython(ctx context.Context, source string) (int64, error) {
	absolute, err := filepath.Abs(source)
	if err != nil {
		return 0, err
	}
	uriPath := filepath.ToSlash(absolute)
	if !strings.HasPrefix(uriPath, "/") {
		uriPath = "/" + uriPath
	}
	uri := (&url.URL{Scheme: "file", Path: uriPath, RawQuery: "mode=ro"}).String()
	if _, err = l.db.ExecContext(ctx, "ATTACH DATABASE ? AS legacy", uri); err != nil {
		return 0, err
	}
	defer l.db.Exec("DETACH DATABASE legacy")
	var sourceRoot string
	if err = l.db.QueryRowContext(ctx, "SELECT value FROM legacy.settings WHERE key='root'").Scan(&sourceRoot); err != nil {
		return 0, err
	}
	resolved, err := filepath.EvalSymlinks(sourceRoot)
	if err != nil {
		return 0, err
	}
	resolved, err = filepath.Abs(resolved)
	if err != nil {
		return 0, err
	}
	if resolved != l.rootPath {
		return 0, fmt.Errorf("source index belongs to a different root")
	}
	result, err := l.db.ExecContext(ctx, `INSERT OR IGNORE INTO images(id,path,directory,signature,metadata,search)
SELECT id,path,directory,'python:'||signature,
metadata_pack(json_set(metadata,'$.id',id,'$.image_url','/api/images/'||id||'/image','$.original_url','/api/images/'||id||'/original')),metadata_search(metadata)
FROM legacy.images AS legacy_row WHERE NOT EXISTS(SELECT 1 FROM main.images AS existing WHERE existing.id=legacy_row.id)`)
	if err != nil {
		return 0, err
	}
	return result.RowsAffected()
}
