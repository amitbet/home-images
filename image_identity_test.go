package main

import (
	"bytes"
	"context"
	"database/sql"
	"os"
	"path/filepath"
	"testing"
)

func TestIdentityMigrationIsAdditiveAndIdempotent(t *testing.T) {
	db := openSQLite(filepath.Join(t.TempDir(), "catalog.sqlite3"), nil, false)
	defer db.Close()
	if _, err := db.Exec(`CREATE TABLE images(id TEXT PRIMARY KEY,path TEXT UNIQUE,signature TEXT,metadata BLOB);INSERT INTO images VALUES('old-id','photo.jpg','old-signature',x'010203');`); err != nil {
		t.Fatal(err)
	}
	for range 2 {
		if err := ensureImageIdentitySchema(db); err != nil {
			t.Fatal(err)
		}
	}
	var id, sig string
	var metadata []byte
	var content, file sql.NullString
	if err := db.QueryRow(`SELECT id,signature,metadata,content_id,file_hash FROM images`).Scan(&id, &sig, &metadata, &content, &file); err != nil {
		t.Fatal(err)
	}
	if id != "old-id" || sig != "old-signature" || !bytes.Equal(metadata, []byte{1, 2, 3}) || content.Valid || file.Valid {
		t.Fatal("migration changed existing record")
	}
}

func TestIdentityBackfillPreservesIndexAndLinksDuplicates(t *testing.T) {
	l, s, _ := fixture(t)
	ctx := context.Background()
	j := testJob(t, l, "a.jpg")
	if err := l.ai.save(ctx, j, syntheticPrediction()); err != nil {
		t.Fatal(err)
	}
	if postTest(t, s, "/api/curation/"+j.ID, `{"favorite":true,"tags":["Family"]}`) != 200 {
		t.Fatal("curation")
	}
	var before []byte
	if err := l.db.QueryRow(`SELECT metadata FROM images WHERE id=?`, j.ID).Scan(&before); err != nil {
		t.Fatal(err)
	}
	if n, err := l.hashIdentityBatch(ctx); err != nil || n != 5 {
		t.Fatalf("backfill %d %v", n, err)
	}
	if n, err := l.hashIdentityBatch(ctx); err != nil || n != 0 {
		t.Fatalf("not resumable %d %v", n, err)
	}
	var after []byte
	var sig string
	if err := l.db.QueryRow(`SELECT metadata,signature FROM images WHERE id=?`, j.ID).Scan(&after, &sig); err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(before, after) || sig != j.Signature {
		t.Fatal("index metadata or signature changed")
	}
	var faces int
	if err := l.ai.db.QueryRow(`SELECT count(*) FROM faces WHERE image_id=?`, j.ID).Scan(&faces); err != nil || faces != 1 {
		t.Fatal("faces lost", err)
	}
	jobs, err := l.ai.jobs(ctx, "")
	if err != nil {
		t.Fatal(err)
	}
	for _, job := range jobs {
		if job.ID == j.ID {
			t.Fatal("AI processing repeated")
		}
	}
	var annotation annotation
	getJSON(t, s, "/api/curation/"+j.ID, &annotation)
	if !annotation.Favorite || len(annotation.Tags) != 1 {
		t.Fatal("annotation lost")
	}
	var result struct {
		ContentID, FileHash string
		Pending             bool
		Matches             []struct{ ID string } `json:"potential_duplicates"`
	}
	// Explicit JSON names for underscored fields.
	var response map[string]any
	if getJSON(t, s, "/api/images/"+j.ID+"/identity", &response) != 200 || response["content_id"] == nil || response["file_hash"] == nil || response["pending"] != false {
		t.Fatal(response)
	}
	if getJSON(t, s, "/api/images/"+j.ID+"/identity", &result) != 200 || len(result.Matches) != 4 {
		t.Fatal("duplicates missing", result)
	}
	postTest(t, s, "/api/curation/"+imageID("b.jpg"), `{"hidden":true}`)
	getJSON(t, s, "/api/images/"+j.ID+"/identity", &result)
	if len(result.Matches) != 3 {
		t.Fatal("hidden duplicate exposed")
	}
	if getJSON(t, s, "/api/images/"+imageID("b.jpg")+"/identity", nil) != 404 {
		t.Fatal("hidden source exposed")
	}
	// No hashes are cleared on an unchanged scan, and no old IDs are replaced.
	l.Scan(ctx)
	if n, err := l.hashIdentityBatch(ctx); err != nil || n != 0 {
		t.Fatal("unchanged scan rehashed", n, err)
	}
	if _, err = l.db.Exec(`UPDATE images SET signature='changed' WHERE id=?`, j.ID); err != nil {
		t.Fatal(err)
	}
	getJSON(t, s, "/api/images/"+j.ID+"/identity", &response)
	if response["pending"] != true || response["content_id"] != nil || response["file_hash"] != nil {
		t.Fatal("stale identity exposed", response)
	}
}

func TestPhoneImportStreamsHashesAndRetainsMetadataVersions(t *testing.T) {
	l, _, _ := fixture(t)
	ctx := context.Background()
	b := l.backup
	phone := backupPhone{ID: "phone", Directory: "Android"}
	folder := backupFolder{ID: "camera", Directory: "camera"}
	incoming := filepath.Join(b.base, "incoming", folder.Directory)
	if err := os.MkdirAll(incoming, 0700); err != nil {
		t.Fatal(err)
	}
	original, err := os.ReadFile("testdata/orientation6.jpg")
	if err != nil {
		t.Fatal(err)
	}
	for name, data := range map[string][]byte{"one.jpg": original, "two.jpg": jpegMetadata(original, "Exif\x00\x00new GPS")} {
		if err = os.WriteFile(filepath.Join(incoming, name), data, 0600); err != nil {
			t.Fatal(err)
		}
	}
	if n, e := b.importFolder(ctx, phone, folder); e != nil || n != 2 {
		t.Fatalf("import %d %v", n, e)
	}
	var count, contents, files int
	if err = l.db.QueryRow(`SELECT count(*),count(DISTINCT content_id),count(DISTINCT file_hash) FROM phone_backup_hashes`).Scan(&count, &contents, &files); err != nil || count != 2 || contents != 1 || files != 2 {
		t.Fatalf("archive hashes %d %d %d %v", count, contents, files, err)
	}
	l.Scan(ctx)
	if _, err = l.hashIdentityBatch(ctx); err != nil {
		t.Fatal(err)
	}
	if err = l.db.QueryRow(`SELECT count(*) FROM images WHERE path LIKE 'Phone backups/%' AND hash_signature=signature AND content_id IS NOT NULL`).Scan(&count); err != nil || count != 2 {
		t.Fatal("imported hashes not reused", count, err)
	}
	// A staging deletion preserves the originals and their identity records.
	if err = os.RemoveAll(incoming); err != nil {
		t.Fatal(err)
	}
	l.Scan(ctx)
	if err = l.db.QueryRow(`SELECT count(*) FROM images WHERE path LIKE 'Phone backups/%'`).Scan(&count); err != nil || count != 2 {
		t.Fatal("archived versions lost")
	}
}

func TestIdentityBackfillRetriesFailuresWithoutStarvingOtherFiles(t *testing.T) {
	l, _, root := fixture(t)
	if err := os.Remove(filepath.Join(root, "a.jpg")); err != nil {
		t.Fatal(err)
	}
	if n, err := l.hashIdentityBatch(context.Background()); err != nil || n != 5 {
		t.Fatal(n, err)
	}
	var remaining, retries int
	if err := l.db.QueryRow(`SELECT count(*) FROM images WHERE hash_signature IS NULL`).Scan(&remaining); err != nil {
		t.Fatal(err)
	}
	if err := l.db.QueryRow(`SELECT count(*) FROM images WHERE hash_retry_at>0`).Scan(&retries); err != nil {
		t.Fatal(err)
	}
	if remaining != 1 || retries != 1 {
		t.Fatal("failure blocked backfill", remaining, retries)
	}
	if n, err := l.hashIdentityBatch(context.Background()); err != nil || n != 0 {
		t.Fatal("retry delay ignored", n, err)
	}
}

func TestIdentityProgressSurvivesRestartAndRejectsStaleResults(t *testing.T) {
	l, _, _ := fixture(t)
	ctx := context.Background()
	j := testJob(t, l, "a.jpg")
	original := identityJob{ID: j.ID, Path: "a.jpg", Signature: j.Signature}
	if err := l.hashIdentity(ctx, original); err != nil {
		t.Fatal(err)
	}
	reopened, err := newLibrary(l.config)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	if n, e := reopened.hashIdentityBatch(ctx); e != nil || n != 4 {
		t.Fatalf("restart repeated completed work: %d %v", n, e)
	}
	// A result computed from an old version must not attach to a replacement.
	if _, err = l.db.Exec(`UPDATE images SET signature='replacement' WHERE id=?`, j.ID); err != nil {
		t.Fatal(err)
	}
	if err = l.saveIdentity(ctx, original, originalHashes{ContentID: "stale", FileHash: "stale"}, ""); err != nil {
		t.Fatal(err)
	}
	var hash sql.NullString
	if err = l.db.QueryRow(`SELECT content_id FROM images WHERE id=?`, j.ID).Scan(&hash); err != nil || hash.Valid {
		t.Fatal("stale hash accepted", err)
	}
}
