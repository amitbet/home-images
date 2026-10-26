package main

import (
	"archive/zip"
	"context"
	"encoding/json"
	"math"
	"os"
	"path/filepath"
	"testing"
)

func TestGeoNearestAndBounds(t *testing.T) {
	tree := geoTree([]geoCity{{geoPoint(43.77, 11.25), "Florence, Tuscany, Italy"}, {geoPoint(32.08, 34.78), "Tel Aviv, Israel"}, {geoPoint(0, 179.99), "Dateline"}}, 0)
	for _, tc := range []struct {
		lat, lon float64
		want     string
	}{{43.771, 11.251, "Florence, Tuscany, Italy"}, {32.081, 34.781, "Tel Aviv, Israel"}, {0, -179.99, "Dateline"}, {0, 0, ""}, {91, 0, ""}, {math.NaN(), 1, ""}} {
		if got := tree.lookup(tc.lat, tc.lon); got != tc.want {
			t.Fatalf("%+v: %q", tc, got)
		}
	}
}
func TestPhotoGPSFormats(t *testing.T) {
	for _, tc := range []struct {
		raw      string
		lat, lon float64
		ok       bool
	}{
		{`{"GPS":{"Latitude":{"Value":32.1,"Ref":1},"Longitude":{"Value":34.8,"Ref":3}}}`, 32.1, 34.8, true},
		{`{"GPS":{"Latitude":{"Value":32.1,"Ref":2},"Longitude":{"Value":34.8,"Ref":4}}}`, -32.1, -34.8, true},
		{`{"GPS":{"Latitude":{"Value":0,"Ref":0},"Longitude":{"Value":0,"Ref":0}}}`, 0, 0, false},
		{`{"GPS":{"GPSLatitude":[32,6,0],"GPSLatitudeRef":"N","GPSLongitude":[34,48,0],"GPSLongitudeRef":"E"}}`, 32.1, 34.8, true},
		{`{"GPSLatitude":["32/1","6/1","0/1"],"GPSLatitudeRef":"S","GPSLongitude":[34,48,0],"GPSLongitudeRef":"W"}`, -32.1, -34.8, true},
		{`{"GPSLatitude":[32,6,0],"GPSLongitude":[34,48,0]}`, 0, 0, false},
		{`{"GPS":{"Latitude":{"Value":100,"Ref":1},"Longitude":{"Value":34,"Ref":3}}}`, 0, 0, false},
	} {
		p := Photo{}
		if e := json.Unmarshal([]byte(tc.raw), &p.Exif); e != nil {
			t.Fatal(e)
		}
		lat, lon, ok := photoGPS(p)
		if ok != tc.ok || (ok && (math.Abs(lat-tc.lat) > 1e-8 || math.Abs(lon-tc.lon) > 1e-8)) {
			t.Fatalf("%s -> %v %v %v", tc.raw, lat, lon, ok)
		}
	}
}
func TestGeocodeBackfillOverridesAndChangedPhotos(t *testing.T) {
	l, s, _ := fixture(t)
	ctx := context.Background()
	job := testJob(t, l, "a.jpg")
	var p Photo
	json.Unmarshal([]byte(job.Metadata), &p)
	p.Exif = map[string]any{"GPS": map[string]any{"Latitude": map[string]any{"Value": 32.08, "Ref": 1}, "Longitude": map[string]any{"Value": 34.78, "Ref": 3}}}
	raw, _ := json.Marshal(p)
	if _, e := l.db.Exec("UPDATE images SET metadata=? WHERE id=?", string(raw), job.ID); e != nil {
		t.Fatal(e)
	}
	tree := geoTree([]geoCity{{geoPoint(32.08, 34.78), "Tel Aviv, Israel"}}, 0)
	if _, e := l.geocodeBatch(ctx, tree); e != nil {
		t.Fatal(e)
	}
	var a annotation
	if getJSON(t, s, "/api/curation/"+job.ID, &a) != 200 || a.AutoLocation != "Tel Aviv, Israel" || a.Location != "" {
		t.Fatal(a)
	}
	postTest(t, s, "/api/curation/"+job.ID, `{"location":"My favorite beach"}`)
	getJSON(t, s, "/api/curation/"+job.ID, &a)
	if a.Location != "My favorite beach" || a.AutoLocation != "Tel Aviv, Israel" {
		t.Fatal(a)
	}
	if n, e := l.geocodeBatch(ctx, tree); e != nil || n != 0 {
		t.Fatalf("cache not reused: %d %v", n, e)
	}
	l.db.Exec("UPDATE images SET signature='changed' WHERE id=?", job.ID)
	getJSON(t, s, "/api/curation/"+job.ID, &a)
	if a.AutoLocation != "" {
		t.Fatal("stale location leaked", a)
	}
	if n, e := l.geocodeBatch(ctx, tree); e != nil || n != 1 {
		t.Fatalf("did not refresh: %d %v", n, e)
	}
}

func TestLoadGeoDataset(t *testing.T) {
	dir := t.TempDir()
	if e := os.WriteFile(filepath.Join(dir, "countryInfo.txt"), []byte("# comment\nIT\tITA\t380\tIT\tItaly\n"), 0600); e != nil {
		t.Fatal(e)
	}
	if e := os.WriteFile(filepath.Join(dir, "admin1CodesASCII.txt"), []byte("IT.16\tTuscany\tTuscany\t1\n"), 0600); e != nil {
		t.Fatal(e)
	}
	file, e := os.Create(filepath.Join(dir, "cities500.zip"))
	if e != nil {
		t.Fatal(e)
	}
	z := zip.NewWriter(file)
	w, e := z.Create("cities500.txt")
	if e != nil {
		t.Fatal(e)
	}
	_, e = w.Write([]byte("3176959\tFlorence\tFlorence\t\t43.77\t11.25\tP\tPPLA\tIT\t\t16\t\t\t\t1\t\t0\tEurope/Rome\t2026-01-01\n"))
	if e != nil {
		t.Fatal(e)
	}
	if e = z.Close(); e != nil {
		t.Fatal(e)
	}
	file.Close()
	tree, e := loadGeo(dir)
	if e != nil {
		t.Fatal(e)
	}
	if got := tree.lookup(43.77, 11.25); got != "Florence, Tuscany, Italy" {
		t.Fatal(got)
	}
}
