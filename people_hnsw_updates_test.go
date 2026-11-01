package main

import (
	"context"
	"path/filepath"
	"testing"
)

func TestPersonHNSWJournalCatchupAndPersistence(t *testing.T) {
	l, _, _ := fixture(t)
	a := l.ai
	ctx := context.Background()
	insert := func(group any) int64 {
		t.Helper()
		r, e := a.db.Exec("INSERT INTO people(model,embedding,merged_into) VALUES(?,?,?)", l.config.ML.FaceModel, vector{1, 0, 0}.blob(), group)
		if e != nil {
			t.Fatal(e)
		}
		id, _ := r.LastInsertId()
		return id
	}
	first, second := insert(nil), insert(nil)
	index, e := a.loadPersonHNSW(ctx)
	if e != nil {
		t.Fatal(e)
	}
	path := filepath.Join(l.config.DataDir, "people.hnsw")
	disk, e := readPersonHNSW(path)
	if e != nil {
		t.Fatal(e)
	}
	alias := insert(first)
	index, e = a.loadPersonHNSW(ctx)
	if e != nil || len(index.vectors[first]) != 2 {
		t.Fatal("new face missing from live index", e)
	}
	untouched, e := readPersonHNSW(path)
	if e != nil || untouched.Revision != disk.Revision {
		t.Fatal("interactive update rewrote the full disk cache", e)
	}
	if _, e = a.db.Exec("UPDATE people SET merged_into=? WHERE id=?", second, alias); e != nil {
		t.Fatal(e)
	}
	index, e = a.loadPersonHNSW(ctx)
	if e != nil || len(index.vectors[first]) != 1 || len(index.vectors[second]) != 2 {
		t.Fatal("journal merge mapping stale", e)
	}
	a.flushPersonHNSW()
	disk, e = readPersonHNSW(path)
	if e != nil || disk.Revision != index.revision {
		t.Fatal("shutdown cache did not persist latest revision", e)
	}
	loaded, e := restorePersonHNSW(disk)
	if e != nil || len(loaded.vectors[second]) != 2 {
		t.Fatal("persisted merge mapping stale", e)
	}
	// Missing journal history must use the complete SQLite snapshot, never
	// silently advance the revision while losing a face correction.
	if _, e = a.db.Exec("UPDATE people SET merged_into=? WHERE id=?", first, alias); e != nil {
		t.Fatal(e)
	}
	if _, e = a.db.Exec("DELETE FROM people_vector_changes WHERE revision=(SELECT max(revision) FROM people_vector_changes)"); e != nil {
		t.Fatal(e)
	}
	index, e = a.loadPersonHNSW(ctx)
	if e != nil || len(index.vectors[first]) != 2 || len(index.vectors[second]) != 1 {
		t.Fatal("journal gap lost correction", e)
	}
}
