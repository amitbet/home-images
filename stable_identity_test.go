package main

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestStableIdentityOfflineMovePreservesCuration(t *testing.T) {
	l, s, root := fixture(t)
	hashFixture(t, l)
	ctx := context.Background()
	id := imageID("a.jpg")
	if code := postTest(t, s, "/api/curation/"+id, `{"favorite":true,"tags":["keep"]}`); code != 200 {
		t.Fatal(code)
	}
	if err := l.ai.save(ctx, testJob(t, l, "a.jpg"), syntheticPrediction()); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(filepath.Join(root, "a.jpg"), filepath.Join(root, "renamed.jpg")); err != nil {
		t.Fatal(err)
	}
	stamp := time.Now()
	if err := os.Chtimes(filepath.Join(root, "renamed.jpg"), stamp, stamp); err != nil {
		t.Fatal(err)
	}
	l.Scan(ctx)
	p, err := l.get(ctx, id)
	if err != nil || p.Path != "renamed.jpg" {
		t.Fatalf("lost stable ID: %+v %v", p, err)
	}
	var favorite int
	if err = l.db.QueryRow("SELECT favorite FROM curation.annotations WHERE image_id=?", id).Scan(&favorite); err != nil || favorite != 1 {
		t.Fatal("lost favorite", err)
	}
	var n int
	if err = l.ai.db.QueryRow("SELECT count(*) FROM faces WHERE image_id=?", id).Scan(&n); err != nil || n != 1 {
		t.Fatal("lost faces", err)
	}
	var currentSignature, aiSignature string
	if e := l.db.QueryRow("SELECT signature FROM images WHERE id=?", id).Scan(&currentSignature); e != nil {
		t.Fatal(e)
	}
	if e := l.ai.db.QueryRow("SELECT signature FROM ai_images WHERE image_id=?", id).Scan(&aiSignature); e != nil || aiSignature != currentSignature {
		t.Fatal("move invalidated verified AI", e)
	}
	// A different photo placed at the original path must not steal the moved ID.
	different, e := os.ReadFile("testdata/orientation6.jpg")
	if e != nil {
		t.Fatal(e)
	}
	different = append(different, []byte("new file at old path")...)
	if e = os.WriteFile(filepath.Join(root, "a.jpg"), different, 0644); e != nil {
		t.Fatal(e)
	}
	l.Scan(ctx)
	p, err = l.get(ctx, id)
	if err != nil || p.Path != "renamed.jpg" {
		t.Fatal("reused old path stole identity", p, err)
	}
	// Moving a file after a scan observed its absence reconnects its tombstone too.
	data, err := os.ReadFile(filepath.Join(root, "renamed.jpg"))
	if err != nil {
		t.Fatal(err)
	}
	if err = os.Remove(filepath.Join(root, "renamed.jpg")); err != nil {
		t.Fatal(err)
	}
	l.Scan(ctx)
	l.ai.cleanup(ctx)
	if err = os.WriteFile(filepath.Join(root, "returned.jpg"), data, 0644); err != nil {
		t.Fatal(err)
	}
	l.Scan(ctx)
	p, err = l.get(ctx, id)
	if err != nil || p.Path != "returned.jpg" {
		t.Fatal("tombstone not reconnected", p, err)
	}
}
func TestDuplicateQuarantineRestoreAndVerification(t *testing.T) {
	l, s, root := fixture(t)
	hashFixture(t, l)
	var groups struct {
		Items []struct{ Files []duplicateFile }
		Total int
	}
	if getJSON(t, s, "/api/duplicates", &groups) != 200 || groups.Total != 1 {
		t.Fatalf("groups %+v", groups)
	}
	keep, remove := imageID("a.jpg"), imageID("b.jpg")
	body, _ := json.Marshal(map[string]string{"keep": keep, "remove": remove})
	if code := postTest(t, s, "/api/duplicates/quarantine-copy", string(body)); code != 200 {
		t.Fatal(code)
	}
	if _, err := os.Stat(filepath.Join(root, "a.jpg")); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(root, "b.jpg")); !os.IsNotExist(err) {
		t.Fatal("copy not quarantined", err)
	}
	if code := postTest(t, s, "/api/duplicates/quarantine-copy", string(body)); code != 409 {
		t.Fatal("stale location allowed cleanup", code)
	}
	l.Scan(context.Background())
	var q struct{ Items []struct{ Token string } }
	if getJSON(t, s, "/api/duplicates/quarantine", &q) != 200 || len(q.Items) != 1 {
		t.Fatal(q)
	}
	restore, _ := json.Marshal(map[string]string{"token": q.Items[0].Token})
	if code := postTest(t, s, "/api/duplicates/restore", string(restore)); code != 200 {
		t.Fatal(code)
	}
	if p, err := l.get(context.Background(), remove); err != nil || p.Path != "b.jpg" {
		t.Fatal("restore lost ID", p, err)
	}
	// Change the selected source after listing: cached hashes must never authorize removal.
	f, err := os.OpenFile(filepath.Join(root, "b.jpg"), os.O_APPEND|os.O_WRONLY, 0)
	if err != nil {
		t.Fatal(err)
	}
	f.Write([]byte("different bytes"))
	f.Close()
	if code := postTest(t, s, "/api/duplicates/quarantine-copy", string(body)); code != 409 {
		t.Fatal("changed file accepted", code)
	}
	if code := postTest(t, s, "/api/duplicates/quarantine-copy", `{"keep":"`+keep+`","remove":"`+keep+`"}`); code != 400 {
		t.Fatal(code)
	}
}
func TestExplicitRootRelocationPreservesLibrary(t *testing.T) {
	l, s, _ := fixture(t)
	// Relocation requires the stored canonical path (macOS /var resolves to /private/var).
	root := l.rootPath
	hashFixture(t, l)
	id := imageID("a.jpg")
	if postTest(t, s, "/api/curation/"+id, `{"favorite":true}`) != 200 {
		t.Fatal("favorite")
	}
	cfg := l.config
	l.Close()
	newRoot := root + "-moved"
	if err := os.Rename(root, newRoot); err != nil {
		t.Fatal(err)
	}
	cfg.Root = newRoot
	cfg.RelocateFrom = root
	moved, err := newLibrary(cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer moved.Close()
	var n int
	if err = moved.db.QueryRow("SELECT favorite FROM curation.annotations WHERE root=? AND image_id=?", moved.rootPath, id).Scan(&n); err != nil || n != 1 {
		t.Fatal("relocation lost favorite", err)
	}
	p, err := moved.get(context.Background(), id)
	if err != nil || p.AbsolutePath != filepath.Join(newRoot, "a.jpg") {
		t.Fatal(p, err)
	}
}

func hashFixture(t *testing.T, l *Library) {
	t.Helper()
	if _, err := l.hashIdentityBatch(context.Background()); err != nil {
		t.Fatal(err)
	}
}

func TestPermanentDuplicateDeletionRequiresRetainedOriginal(t *testing.T) {
	l, s, root := fixture(t)
	hashFixture(t, l)
	keep, remove := imageID("a.jpg"), imageID("b.jpg")
	if postTest(t, s, "/api/curation/"+remove, `{"favorite":true,"tags":["family"]}`) != 200 {
		t.Fatal("favorite")
	}
	body, _ := json.Marshal(map[string]string{"keep": keep, "remove": remove})
	if code := postTest(t, s, "/api/duplicates/quarantine-copy", string(body)); code != 200 {
		t.Fatal(code)
	}
	var favorite int
	if e := l.db.QueryRow("SELECT favorite FROM curation.annotations WHERE root=? AND image_id=?", l.rootPath, keep).Scan(&favorite); e != nil || favorite != 1 {
		t.Fatal("favorite not carried to kept photo", e)
	}
	var token, dest string
	if e := l.db.QueryRow("SELECT token,quarantine_path FROM duplicate_quarantine").Scan(&token, &dest); e != nil {
		t.Fatal(e)
	}
	payload, _ := json.Marshal(map[string]string{"token": token})
	original, e := os.ReadFile(filepath.Join(root, "a.jpg"))
	if e != nil {
		t.Fatal(e)
	}
	if e = os.Remove(filepath.Join(root, "a.jpg")); e != nil {
		t.Fatal(e)
	}
	if code := postTest(t, s, "/api/duplicates/purge", string(payload)); code != 409 {
		t.Fatal("deleted without keeper", code)
	}
	if _, e = os.Stat(filepath.Join(root, filepath.FromSlash(dest))); e != nil {
		t.Fatal("quarantine lost", e)
	}
	if e = os.WriteFile(filepath.Join(root, "a.jpg"), original, 0644); e != nil {
		t.Fatal(e)
	}
	if code := postTest(t, s, "/api/duplicates/purge", string(payload)); code != 200 {
		t.Fatal(code)
	}
	if _, e = os.Stat(filepath.Join(root, filepath.FromSlash(dest))); !os.IsNotExist(e) {
		t.Fatal("quarantined copy still exists", e)
	}
	if _, e = os.Stat(filepath.Join(root, "a.jpg")); e != nil {
		t.Fatal("keeper deleted", e)
	}
}
