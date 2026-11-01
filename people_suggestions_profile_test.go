package main

import (
	"io"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// Open production SQLite files mode=ro. Only temporary covering indexes and the
// disposable HNSW cache are written; production data stays untouched.
func TestProfileLivePeopleSuggestions(t *testing.T) {
	path := os.Getenv("HOMEIMAGES_PROFILE_CONFIG")
	if path == "" {
		t.Skip("set HOMEIMAGES_PROFILE_CONFIG to profile live suggestions read-only")
	}
	c, err := loadConfig(path)
	if err != nil {
		t.Fatal(err)
	}
	dataDir := c.DataDir
	c.DataDir = t.TempDir()
	src, err := os.Open(filepath.Join(dataDir, "people.hnsw"))
	if err != nil {
		t.Fatal(err)
	}
	dst, err := os.Create(filepath.Join(c.DataDir, "people.hnsw"))
	if err != nil {
		t.Fatal(err)
	}
	_, err = io.Copy(dst, src)
	src.Close()
	dst.Close()
	if err != nil {
		t.Fatal(err)
	}
	l := &Library{config: c, rootPath: c.Root}
	a := &AI{lib: l, models: c.ML.ClipModel + "|" + c.ML.FaceModel, suggestionSlot: make(chan struct{}, 1)}
	ro := func(path string) string {
		p := filepath.ToSlash(path)
		if filepath.VolumeName(path) != "" {
			p = "/" + p
		}
		return (&url.URL{Scheme: "file", Path: p, RawQuery: "mode=ro"}).String()
	}
	a.read = openSQLite(ro(filepath.Join(dataDir, "ai.sqlite3")), map[string]string{"catalog": ro(filepath.Join(dataDir, "index-go.sqlite3")), "curation": ro(filepath.Join(dataDir, "curation.sqlite3"))}, false)
	a.read.SetMaxOpenConns(1)
	defer a.read.Close()
	// Main and attached files are opened mode=ro. Temporary covering indexes
	// reproduce the schema upgrade without touching the installed databases.
	if _, err = a.read.Exec(`CREATE TEMP TABLE faces AS SELECT id,image_id,person_id,box FROM main.faces;
CREATE INDEX temp.face_colocation_person ON faces(person_id,image_id,json_extract(box,'$.timestamp'));
CREATE INDEX temp.face_colocation_image ON faces(image_id,json_extract(box,'$.timestamp'),person_id);`); err != nil {
		t.Fatal(err)
	}
	person := os.Getenv("HOMEIMAGES_PROFILE_PERSON")
	if person == "" {
		person = "956"
	}
	queries := []string{"limit=25&preview=true&min_score=0&media_type=all", "limit=25&preview=true&min_score=0&media_type=all", "limit=1000&min_score=0&media_type=all"}
	if os.Getenv("HOMEIMAGES_PROFILE_EXACT") == "1" {
		queries = append(queries, "limit=1000&min_score=0&media_type=all&exact=true")
	}
	for _, query := range queries {
		r := httptest.NewRequest("GET", "/api/people/"+person+"/suggestions?"+query, nil)
		r.SetPathValue("person", person)
		w := httptest.NewRecorder()
		started := time.Now()
		if err = a.personSuggestions(w, r); err != nil {
			t.Fatal(err)
		}
		t.Logf("%s: %s; %s; response %d bytes", query, time.Since(started), w.Header().Get("Server-Timing"), w.Body.Len())
	}
}
