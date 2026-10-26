package main

import (
	"bytes"
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"
)

func TestCatalogMigrationPreservesMetadataAndSearch(t *testing.T) {
	db := openSQLite(filepath.Join(t.TempDir(), "catalog.sqlite3"), nil, false)
	defer db.Close()
	_, err := db.Exec(`CREATE TABLE images(id TEXT PRIMARY KEY,path TEXT UNIQUE NOT NULL,directory TEXT NOT NULL,signature TEXT NOT NULL,metadata TEXT NOT NULL,search TEXT NOT NULL)`)
	if err != nil {
		t.Fatal(err)
	}
	raw, _ := json.Marshal(map[string]any{"path": "family/birthday.jpg", "taken_at": "2020:03:04 12:13:14", "created_at": "2020-03-04", "size_bytes": 123, "exif": map[string]any{"ImageDescription": "Birthday dog on beach יום הולדת", "Photo": map[string]any{"UserComment": strings.Repeat("\x00\x01", 10000)}, "Keywords": []any{"party", "cake"}}})
	if _, err = db.Exec("INSERT INTO images VALUES('id','family/birthday.jpg','family','sig',?,?)", string(raw), strings.ToLower(string(raw))); err != nil {
		t.Fatal(err)
	}
	if err = migrateCatalog(db); err != nil {
		t.Fatal(err)
	}
	var blob []byte
	var search, taken, created string
	var size, version int
	if err = db.QueryRow("SELECT metadata,search,taken_at,created_at,size_bytes FROM images").Scan(&blob, &search, &taken, &created, &size); err != nil {
		t.Fatal(err)
	}
	decoded, err := metadataJSON(blob)
	if err != nil || decoded != string(raw) {
		t.Fatal("metadata changed", err)
	}
	if !bytes.HasPrefix(blob, metadataMagic) || len(blob) >= len(raw)/2 {
		t.Fatal("not compressed")
	}
	for _, term := range []string{"birthday", "dog", "beach", "יום הולדת", "party", "cake"} {
		if !strings.Contains(search, term) {
			t.Fatal("missing search term", term)
		}
	}
	if len(search) > 500 {
		t.Fatal("binary payload indexed", len(search))
	}
	if taken != "2020:03:04 12:13:14" || created != "2020-03-04" || size != 123 {
		t.Fatal("derived fields", taken, created, size)
	}
	if err = migrateCatalog(db); err != nil {
		t.Fatal("migration not idempotent", err)
	}
	db.QueryRow("PRAGMA user_version").Scan(&version)
	if version != catalogVersion {
		t.Fatal(version)
	}
	if _, err = db.Exec(`UPDATE images SET metadata=metadata_pack('{"taken_at":"2026-01-01","size_bytes":456}')`); err != nil {
		t.Fatal(err)
	}
	if err = db.QueryRow("SELECT taken_at,size_bytes FROM images").Scan(&taken, &size); err != nil || taken != "2026-01-01" || size != 456 {
		t.Fatal("derived fields not refreshed", err)
	}
	// Replacement connections must retain codec functions.
	db.SetMaxIdleConns(0)
	if err = db.QueryRow("SELECT metadata_json(metadata) FROM images").Scan(&decoded); err != nil {
		t.Fatal(err)
	}
}

func TestCatalogMigrationRollsBackAndRejectsFuture(t *testing.T) {
	db := openSQLite(filepath.Join(t.TempDir(), "catalog.sqlite3"), nil, false)
	defer db.Close()
	if _, err := db.Exec(`CREATE TABLE images(id TEXT PRIMARY KEY,metadata TEXT,search TEXT); INSERT INTO images VALUES('bad','invalid json','old')`); err != nil {
		t.Fatal(err)
	}
	if err := migrateCatalog(db); err == nil {
		t.Fatal("invalid metadata migrated")
	}
	var version int
	db.QueryRow("PRAGMA user_version").Scan(&version)
	if version != 0 {
		t.Fatal("version advanced on failure")
	}
	if _, err := db.Exec("SELECT taken_at FROM images"); err == nil {
		t.Fatal("schema not rolled back")
	}
	db.Exec("PRAGMA user_version=999")
	if err := migrateCatalog(db); err == nil {
		t.Fatal("future version accepted")
	}
}

func TestCatalogScanStoresCompressedMetadata(t *testing.T) {
	l, s, _ := fixture(t)
	var kind string
	if err := l.db.QueryRow("SELECT typeof(metadata) FROM images LIMIT 1").Scan(&kind); err != nil || kind != "blob" {
		t.Fatal("scan did not compress", kind, err)
	}
	var health struct {
		Version int `json:"catalog_version"`
	}
	getJSON(t, s, "/health", &health)
	if health.Version != catalogVersion {
		t.Fatal("health version", health)
	}
	var result struct {
		Total int `json:"total"`
	}
	getJSON(t, s, "/api/search?mode=metadata&q=Synthetic", &result)
	if result.Total != 5 {
		t.Fatal("camera text search", result)
	}
}
