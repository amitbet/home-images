package main

import (
	"context"
	"testing"
)

func TestPersonNameAutocomplete(t *testing.T) {
	l, s, _ := fixture(t)
	for n, path := range []string{"a.jpg", "b.jpg", "album_100%/c.jpg"} {
		prediction := syntheticPrediction()
		if n == 1 {
			prediction.Faces[0].Embedding = []byte(`"[0,1,0]"`)
		} else if n == 2 {
			prediction.Faces[0].Embedding = []byte(`"[0,0,1]"`)
		}
		if err := l.ai.save(context.Background(), testJob(t, l, path), prediction); err != nil {
			t.Fatal(err)
		}
	}
	setName := func(path, name string) {
		t.Helper()
		if _, err := l.ai.db.Exec("UPDATE people SET name=? WHERE id=(SELECT person_id FROM faces WHERE image_id=?)", name, imageID(path)); err != nil {
			t.Fatal(err)
		}
	}
	setName("a.jpg", "Yehudit")
	setName("b.jpg", "Yehudit hidden")
	setName("album_100%/c.jpg", "100%_! literal")
	postTest(t, s, "/api/curation/"+imageID("b.jpg"), `{"hidden":true}`)
	check := func(query string, names ...string) {
		t.Helper()
		var result struct {
			Items []struct {
				ID   int64
				Name string
			}
		}
		if status := getJSON(t, s, "/api/people/names?"+query, &result); status != 200 || len(result.Items) != len(names) {
			t.Fatalf("%s: status=%d items=%+v", query, status, result.Items)
		}
		for i, name := range names {
			if result.Items[i].Name != name || result.Items[i].ID == 0 {
				t.Fatalf("%s: %+v", query, result.Items)
			}
		}
	}
	check("q=yEhU", "Yehudit")
	check("q=yehu&hidden=all", "Yehudit", "Yehudit hidden")
	check("q=yehu&hidden=only", "Yehudit hidden")
	check("q=%25_!", "100%_! literal")
	check("limit=1", "100%_! literal")
	setName("a.jpg", "Renamed")
	check("q=yehu")
	check("q=renamed", "Renamed")
	setName("a.jpg", "")
	check("q=yehu&hidden=all", "Yehudit hidden")
	setName("a.jpg", "Renamed")
	if _, err := l.ai.db.Exec("UPDATE people SET merged_into=999 WHERE name='Renamed'"); err != nil {
		t.Fatal(err)
	}
	check("q=renamed")
	if _, err := l.ai.db.Exec("UPDATE people SET merged_into=NULL WHERE name='Renamed'"); err != nil {
		t.Fatal(err)
	}
	if _, err := l.db.Exec("UPDATE images SET signature='changed' WHERE id=?", imageID("a.jpg")); err != nil {
		t.Fatal(err)
	}
	check("q=renamed")
	for query, status := range map[string]int{"limit=0": 422, "hidden=bad": 400} {
		var result map[string]any
		if getJSON(t, s, "/api/people/names?"+query, &result) != status {
			t.Fatal(query)
		}
	}
}
