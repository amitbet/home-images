package main

import (
	"context"
	"testing"
)

// The idle cleanup checks on the read pool before writing; it must still
// delete AI rows for images that left the catalog, and keep live ones.
func TestAICleanupRemovesOnlyOrphans(t *testing.T) {
	l, _, _ := fixture(t)
	ctx := context.Background()
	var live string
	if err := l.db.QueryRow("SELECT id FROM images LIMIT 1").Scan(&live); err != nil {
		t.Fatal(err)
	}
	l.ai.cleanup(ctx) // nothing orphaned: no-op
	for _, id := range []string{live, "gone"} {
		if _, err := l.ai.db.Exec("INSERT INTO ai_images(image_id,signature,models,error,retry_at) VALUES(?,'s','m','',0)", id); err != nil {
			t.Fatal(err)
		}
		if _, err := l.ai.db.Exec("INSERT INTO faces(image_id,person_id,box,score,width,height) VALUES(?,1,'{}',1,1,1)", id); err != nil {
			t.Fatal(err)
		}
	}
	l.ai.cleanup(ctx)
	for _, table := range []string{"ai_images", "faces"} {
		var gone, kept int
		if err := l.ai.db.QueryRow("SELECT count(*) FILTER (WHERE image_id='gone'),count(*) FILTER (WHERE image_id=?) FROM "+table, live).Scan(&gone, &kept); err != nil {
			t.Fatal(err)
		}
		if gone != 0 || kept != 1 {
			t.Fatalf("%s: orphan rows %d, live rows %d", table, gone, kept)
		}
	}
}
