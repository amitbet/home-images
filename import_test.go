package main

import (
	"context"
	"database/sql"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

func TestPythonImport(t *testing.T) {
	l, _, root := fixture(t)
	source := filepath.Join(t.TempDir(), "legacy index.sqlite3")
	db, e := sql.Open("sqlite", source)
	if e != nil {
		t.Fatal(e)
	}
	defer db.Close()
	_, e = db.Exec(`CREATE TABLE settings(key TEXT PRIMARY KEY,value TEXT);CREATE TABLE images(id TEXT PRIMARY KEY,path TEXT,directory TEXT,signature TEXT,metadata TEXT,search TEXT)`)
	if e != nil {
		t.Fatal(e)
	}
	_, e = db.Exec("INSERT INTO settings VALUES('root',?)", root)
	if e != nil {
		t.Fatal(e)
	}
	data, e := os.ReadFile("testdata/orientation1.jpg")
	if e != nil {
		t.Fatal(e)
	}
	if e = os.WriteFile(filepath.Join(root, "imported.jpg"), data, 0644); e != nil {
		t.Fatal(e)
	}
	metadata := map[string]any{"path": "imported.jpg", "width": 96, "height": 64, "exif": map[string]any{"Make": "Original Camera"}}
	encoded, _ := json.Marshal(metadata)
	_, e = db.Exec("INSERT INTO images VALUES(?,?,?,?,?,?)", imageID("imported.jpg"), "imported.jpg", "", "legacy-signature", string(encoded), "original camera")
	if e != nil {
		t.Fatal(e)
	}
	count, e := l.ImportPython(context.Background(), source)
	if e != nil || count != 1 {
		t.Fatalf("import: %d %v", count, e)
	}
	p, e := l.get(context.Background(), imageID("imported.jpg"))
	if e != nil || p.ID == "" || p.ImageURL == "" || p.Exif["Make"] != "Original Camera" {
		t.Fatalf("imported data: %+v %v", p, e)
	}
	var original string
	if e = db.QueryRow("SELECT metadata FROM images").Scan(&original); e != nil || original != string(encoded) {
		t.Fatal("source was modified")
	}
	count, e = l.ImportPython(context.Background(), source)
	if e != nil || count != 0 {
		t.Fatal("import is not idempotent")
	}
	l.Scan(context.Background())
	p, e = l.get(context.Background(), imageID("imported.jpg"))
	if e != nil || p.Exif["Make"] != "Synthetic Camera" {
		t.Fatal("imported metadata was not refreshed")
	}
	if _, e = db.Exec("UPDATE settings SET value=?", t.TempDir()); e != nil {
		t.Fatal(e)
	}
	if _, e = l.ImportPython(context.Background(), source); e == nil {
		t.Fatal("foreign root imported")
	}
}
