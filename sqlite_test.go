package main

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"errors"
	"testing"
	"time"
)

func TestSQLiteReplacementRestoresAttachedDatabases(t *testing.T) {
	l, s, _ := fixture(t)
	id := testJob(t, l, "a.jpg").ID
	if postTest(t, s, "/api/curation/"+id, `{"favorite":true}`) != 200 {
		t.Fatal("save annotation")
	}
	for name, db := range map[string]*sql.DB{"library": l.db, "ai-writer": l.ai.db, "ai-reader": l.ai.read} {
		t.Run(name, func(t *testing.T) {
			// Force database/sql to discard the physical connection, as can happen
			// after a browser cancels an expensive search.
			conn, err := db.Conn(context.Background())
			if err != nil {
				t.Fatal(err)
			}
			if err = conn.Raw(func(any) error { return driver.ErrBadConn }); !errors.Is(err, driver.ErrBadConn) {
				t.Fatal(err)
			}
			_ = conn.Close()
			var count int
			if err = db.QueryRow("SELECT count(*) FROM curation.annotations").Scan(&count); err != nil || count != 1 {
				t.Fatal("curation attachment lost", count, err)
			}
			if name != "library" {
				if err = db.QueryRow("SELECT count(*) FROM catalog.images").Scan(&count); err != nil || count != 5 {
					t.Fatal("catalog attachment lost", count, err)
				}
			}
			if name == "ai-reader" {
				if _, err = db.Exec("UPDATE curation.annotations SET favorite=0"); err == nil {
					t.Fatal("query_only was lost on replacement")
				}
			}
		})
	}
	var result struct{ Total int }
	if getJSON(t, s, "/api/search?mode=metadata&favorite=true", &result) != 200 || result.Total != 1 {
		t.Fatal("UI search failed after connection replacement", result)
	}
	for _, path := range []string{"/api/albums", "/api/collections", "/api/indexing"} {
		if getJSON(t, s, path, nil) != 200 {
			t.Fatal("UI endpoint failed after replacement", path)
		}
	}
}

func TestSQLiteCancelledQueryKeepsUIAvailable(t *testing.T) {
	l, s, _ := fixture(t)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Millisecond)
	defer cancel()
	var sum int64
	err := l.ai.read.QueryRowContext(ctx, `WITH RECURSIVE counter(x) AS (VALUES(1) UNION ALL SELECT x+1 FROM counter WHERE x<100000000) SELECT sum(x) FROM counter`).Scan(&sum)
	if err == nil {
		t.Fatal("expected expensive query to be canceled")
	}
	var result struct{ Total int }
	if getJSON(t, s, "/api/search?mode=metadata", &result) != 200 || result.Total != 5 {
		t.Fatal("search broken after cancel", result)
	}
	for _, path := range []string{"/api/albums", "/api/collections", "/api/indexing"} {
		if getJSON(t, s, path, nil) != 200 {
			t.Fatal("UI endpoint failed after canceled query", path)
		}
	}
}
