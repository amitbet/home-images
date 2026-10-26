package main

import (
	"database/sql"
	"fmt"
)

// Match the sort expression and its fixed ascending filename tie-breakers.
// A descending scan of an ascending index also reverses the tie-breakers, so
// keep both directions to avoid sorting large result sets again.
func ensurePhotoSortIndexes(db *sql.DB) error {
	for _, key := range []struct{ name, expr string }{
		{"taken", "nullif(taken_at,'')"},
		{"created", "created_at"},
		{"size", "size_bytes"},
	} {
		for _, direction := range []string{"ASC", "DESC"} {
			query := fmt.Sprintf("CREATE INDEX IF NOT EXISTS photo_sort_%s_%s ON images(%s COLLATE NOCASE %s,path COLLATE NOCASE,path)", key.name, direction, key.expr, direction)
			if _, err := db.Exec(query); err != nil {
				return err
			}
		}
	}
	return nil
}
