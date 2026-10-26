package main

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestCurationFiltersAlbumsAndPersistence(t *testing.T) {
	l, s, _ := fixture(t)
	p := testJob(t, l, "a.jpg")
	q := testJob(t, l, "b.jpg")
	post := func(path, body string, want int) {
		t.Helper()
		if got := postTest(t, s, path, body); got != want {
			t.Fatalf("POST %s = %d, want %d", path, got, want)
		}
	}
	post("/api/curation/"+p.ID, `{"favorite":true,"title":"Holiday memories","location":"Paris 100%","tags":["Family","family"," holiday "]}`, 200)
	post("/api/curation/"+q.ID, `{"hidden":true}`, 200)
	var a annotation
	if getJSON(t, s, "/api/curation/"+p.ID, &a) != 200 || !a.Favorite || a.Location != "Paris 100%" || len(a.Tags) != 2 {
		t.Fatalf("annotation: %+v", a)
	}
	count := func(query string, want int) {
		t.Helper()
		var result struct{ Total int }
		if status := getJSON(t, s, "/api/search?mode=metadata&"+query, &result); status != 200 || result.Total != want {
			t.Fatalf("search %q: status %d, count %d, want %d", query, status, result.Total, want)
		}
	}
	count("favorite=true", 1)
	count("place=100%25", 1)
	count("place=100_", 0)
	count("tag=family", 1)
	count("hidden=only", 1)
	count("", 4)
	count("hidden=all", 5)
	count("q=Holiday", 1)
	var photo Photo
	if getJSON(t, s, "/api/next?favorite=true&mode=sequential", &photo) != 200 || photo.ID != p.ID {
		t.Fatal("favorite playback", photo)
	}
	// Actual /next must never pick the hidden image, including sequential wraparound.
	after := ""
	for range 8 {
		photo = nextPhoto(t, s, "mode=sequential&after="+after)
		if photo.ID == q.ID {
			t.Fatal("hidden photo leaked to playback")
		}
		after = photo.ID
	}
	post("/api/albums", `{"name":"Family holiday"}`, 201)
	var albums struct {
		Items []struct {
			ID         int64
			Name       string
			PhotoCount int `json:"photo_count"`
		}
	}
	getJSON(t, s, "/api/albums", &albums)
	if len(albums.Items) != 1 {
		t.Fatal(albums)
	}
	id := albums.Items[0].ID
	post(fmt.Sprintf("/api/albums/%d", id), fmt.Sprintf(`{"action":"add","image_id":%q}`, p.ID), 200)
	count(fmt.Sprintf("album=%d", id), 1)
	post(fmt.Sprintf("/api/albums/%d", id), fmt.Sprintf(`{"action":"add","image_id":%q}`, q.ID), 200)
	var covers struct {
		Items []struct {
			Count int    `json:"photo_count"`
			URL   string `json:"image_url"`
		}
	}
	getJSON(t, s, "/api/albums", &covers)
	if covers.Items[0].Count != 1 || !strings.Contains(covers.Items[0].URL, p.ID) {
		t.Fatal("Hidden photo included in album cover/count", covers)
	}
	post("/api/curation/"+p.ID, `{"hidden":true}`, 200)
	getJSON(t, s, "/api/albums", &covers)
	if covers.Items[0].Count != 0 || covers.Items[0].URL != "" {
		t.Fatal("All-hidden album exposed a cover", covers)
	}
	post("/api/curation/"+p.ID, `{"hidden":false}`, 200)
	if getJSON(t, s, fmt.Sprintf("/api/next?album=%d&mode=random", id), &photo) != 200 || photo.ID != p.ID {
		t.Fatal("album playback", photo)
	}
	// Membership is idempotent and deleting an album never deletes photos.
	post(fmt.Sprintf("/api/albums/%d", id), fmt.Sprintf(`{"action":"add","image_id":%q}`, p.ID), 200)
	post("/api/collections", `{"name":"Paris favorites","query":{"favorite":"true","place":"Paris"}}`, 201)
	var saved struct{ Items []map[string]any }
	getJSON(t, s, "/api/collections", &saved)
	if len(saved.Items) != 1 {
		t.Fatal(saved)
	}
	var exported map[string]any
	if getJSON(t, s, "/api/curation/export", &exported) != 200 || len(exported["annotations"].([]any)) != 2 {
		t.Fatal(exported)
	}
	reopened, err := newLibrary(l.config)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	restored, err := reopened.ai.annotation(p.ID)
	if err != nil || restored.Title != "Holiday memories" || len(restored.Albums) != 1 {
		t.Fatal("curation lost on restart", restored, err)
	}
	l.Scan(context.Background())
	count("favorite=true", 1)
	post(fmt.Sprintf("/api/albums/%d", id), `{"action":"delete"}`, 200)
	count("hidden=all", 5)
}

func TestCurationWriteProtectionAndValidation(t *testing.T) {
	l, s, _ := fixture(t)
	id := testJob(t, l, "a.jpg").ID
	for _, tc := range []struct {
		path, body string
		status     int
	}{{"/api/curation/" + id, `{"title":"` + strings.Repeat("x", 151) + `"}`, 400}, {"/api/curation/missing", `{"favorite":true}`, 404}, {"/api/albums", `{"name":" "}`, 400}, {"/api/collections", `{"name":"Bad","query":{"unknown":"x"}}`, 400}} {
		if got := postTest(t, s, tc.path, tc.body); got != tc.status {
			t.Fatalf("%s: %d", tc.path, got)
		}
	}
	for _, query := range []string{"from=not-a-date", "from=2026-12-01&to=2026-01-01", "album=-1", "hidden=oops"} {
		if getJSON(t, s, "/api/search?mode=metadata&"+query, nil) != 400 {
			t.Fatal("invalid filter accepted", query)
		}
	}
	req, _ := http.NewRequest("POST", s.URL+"/api/curation/"+id, strings.NewReader(`{"favorite":true}`))
	req.Header.Set("X-Home-Images", "1")
	req.Header.Set("Origin", "https://other.example")
	res, err := s.Client().Do(req)
	if err != nil {
		t.Fatal(err)
	}
	res.Body.Close()
	if res.StatusCode != 403 {
		t.Fatal("cross-origin write allowed")
	}
	// Annotation text is JSON data, not HTML, including names containing markup.
	name := `<img src=x onerror=alert(1)>`
	body, _ := json.Marshal(map[string]string{"title": name})
	if postTest(t, s, "/api/curation/"+id, string(body)) != 200 {
		t.Fatal("text rejected")
	}
	var a annotation
	getJSON(t, s, "/api/curation/"+id, &a)
	if a.Title != name {
		t.Fatal("text not preserved")
	}
}

func TestCurationRootIsolation(t *testing.T) {
	l, s, root := fixture(t)
	id := testJob(t, l, "a.jpg").ID
	if postTest(t, s, "/api/curation/"+id, `{"favorite":true}`) != 200 {
		t.Fatal("save")
	}
	config := l.config
	config.Root = root + "/album_100%"
	other, err := newLibrary(config)
	if err != nil {
		t.Fatal(err)
	}
	defer other.Close()
	a, err := other.ai.annotation(id)
	if err != nil || a.Favorite {
		t.Fatal("annotations leaked between roots", a, err)
	}
	server := httptest.NewServer(other.Handler())
	defer server.Close()
	var result struct{ Items []any }
	if getJSON(t, server, "/api/albums", &result) != 200 || len(result.Items) != 0 {
		t.Fatal("root-scoped albums")
	}
}
