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
	// Scope first, then the default image-grid sort. These covering indexes avoid
	// sorting or reading every original's compressed metadata on group entry.
	for _, direction := range []string{"ASC", "DESC"} {
		for _, scope := range []struct{ name, expr string }{
			{"directory", "directory"},
			{"year", "coalesce(nullif(substr(coalesce(nullif(taken_at,''),created_at),1,4),''),'Unknown year') COLLATE NOCASE"},
		} {
			query := fmt.Sprintf("CREATE INDEX IF NOT EXISTS photo_scope_%s_taken_%s ON images(%s,nullif(taken_at,'') COLLATE NOCASE %s,path COLLATE NOCASE,path,id)", scope.name, direction, scope.expr, direction)
			if _, err := db.Exec(query); err != nil {
				return err
			}
		}
	}
	return nil
}
