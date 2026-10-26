package main

import (
	"context"
	"path/filepath"
	"testing"
)

func TestImportLocationsMatchingAndRootIsolation(t *testing.T) {
	l, _, _ := fixture(t)
	job := testJob(t, l, "a.jpg")
	source := filepath.Join(t.TempDir(), "locations.sqlite3")
	db := openSQLite(source, nil, false)
	defer db.Close()
	if _, err := db.Exec(`CREATE TABLE settings(key TEXT PRIMARY KEY,value TEXT); CREATE TABLE locations(image_id TEXT PRIMARY KEY,signature TEXT,label TEXT)`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec("INSERT INTO settings VALUES('root',?)", l.rootPath); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec("INSERT INTO locations VALUES(?,?,'Imported city'),('missing','x','Missing')", job.ID, job.Signature); err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	n, err := l.ImportLocations(ctx, source)
	if err != nil || n != 1 {
		t.Fatal(n, err)
	}
	var label string
	if err = l.db.QueryRow("SELECT label FROM locations WHERE image_id=?", job.ID).Scan(&label); err != nil || label != "Imported city" {
		t.Fatal(label, err)
	}
	db.Exec("UPDATE locations SET signature='stale',label='Wrong'")
	n, err = l.ImportLocations(ctx, source)
	if err != nil || n != 0 {
		t.Fatal(n, err)
	}
	l.db.QueryRow("SELECT label FROM locations WHERE image_id=?", job.ID).Scan(&label)
	if label != "Imported city" {
		t.Fatal(label)
	}
	db.Exec("UPDATE settings SET value=? WHERE key='root'", t.TempDir())
	if _, err = l.ImportLocations(ctx, source); err == nil {
		t.Fatal("accepted wrong root")
	}
}

// A photo added after an offline import is still eligible for background geocoding.
func TestGeocodeNewImageAfterLocationImport(t *testing.T) {
	l, _, _ := fixture(t)
	tree := geoTree([]geoCity{{geoPoint(32.08, 34.78), "Tel Aviv, Israel"}}, 0)
	ctx := context.Background()
	if _, err := l.geocodeBatch(ctx, tree); err != nil {
		t.Fatal(err)
	}
	metadata := `{"exif":{"GPS":{"Latitude":{"Value":32.08,"Ref":1},"Longitude":{"Value":34.78,"Ref":3}}}}`
	if _, err := l.db.Exec("INSERT INTO images(id,path,directory,signature,metadata,search) VALUES('arrival','arrival.jpg','','new',metadata_pack(?),'')", metadata); err != nil {
		t.Fatal(err)
	}
	n, err := l.geocodeBatch(ctx, tree)
	if err != nil || n != 1 {
		t.Fatal(n, err)
	}
	var label string
	if err = l.db.QueryRow("SELECT label FROM locations WHERE image_id='arrival'").Scan(&label); err != nil || label != "Tel Aviv, Israel" {
		t.Fatal(label, err)
	}
}
