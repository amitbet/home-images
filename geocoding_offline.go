package main

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
)

func readOnlyCatalog(path string) *sql.DB {
	absolute, _ := filepath.Abs(path)
	uriPath := filepath.ToSlash(absolute)
	if filepath.VolumeName(absolute) != "" {
		uriPath = "/" + uriPath
	}
	u := url.URL{Scheme: "file", Path: uriPath, RawQuery: "mode=ro&immutable=1"}
	return openSQLite(u.String(), nil, true)
}

// Backfill a stopped-service snapshot without accessing originals or changing
// the snapshot. The output contains only derived locations and the root identity.
func backfillLocations(ctx context.Context, input, output, dataset string) (count, located int, err error) {
	if _, e := os.Stat(output); !os.IsNotExist(e) {
		return 0, 0, fmt.Errorf("output must be a new file: %s", output)
	}
	tree, e := loadGeo(dataset)
	if e != nil {
		return 0, 0, e
	}
	source := readOnlyCatalog(input)
	defer source.Close()
	var root string
	if e = source.QueryRowContext(ctx, "SELECT value FROM settings WHERE key='root'").Scan(&root); e != nil {
		return 0, 0, e
	}
	rows, e := source.QueryContext(ctx, "SELECT id,signature,metadata_json(metadata) FROM images ORDER BY id")
	if e != nil {
		return 0, 0, e
	}
	defer rows.Close()
	dest := openSQLite(output, nil, false)
	defer dest.Close()
	if _, e = dest.ExecContext(ctx, `CREATE TABLE locations(image_id TEXT PRIMARY KEY,signature TEXT NOT NULL,label TEXT NOT NULL); CREATE TABLE settings(key TEXT PRIMARY KEY,value TEXT);`); e != nil {
		return 0, 0, e
	}
	if _, e = dest.ExecContext(ctx, "INSERT INTO settings VALUES('root',?)", root); e != nil {
		return 0, 0, e
	}
	tx, e := dest.BeginTx(ctx, nil)
	if e != nil {
		return 0, 0, e
	}
	defer tx.Rollback()
	stmt, e := tx.PrepareContext(ctx, "INSERT INTO locations VALUES(?,?,?)")
	if e != nil {
		return 0, 0, e
	}
	defer stmt.Close()
	for rows.Next() {
		var id, signature, raw string
		var p Photo
		if e = rows.Scan(&id, &signature, &raw); e != nil {
			return 0, 0, e
		}
		if e = json.Unmarshal([]byte(raw), &p); e != nil {
			return 0, 0, e
		}
		label := ""
		if lat, lon, ok := photoGPS(p); ok {
			label = tree.lookup(lat, lon)
		}
		if _, e = stmt.ExecContext(ctx, id, signature, label); e != nil {
			return 0, 0, e
		}
		count++
		if label != "" {
			located++
		}
	}
	if e = rows.Err(); e != nil {
		return 0, 0, e
	}
	if e = tx.Commit(); e != nil {
		return 0, 0, e
	}
	return count, located, nil
}
