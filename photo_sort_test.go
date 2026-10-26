package main

import (
	"context"
	"fmt"
	"net/url"
	"testing"
)

func TestPhotoSortPagination(t *testing.T) {
	l, s, _ := fixture(t)
	p := testJob(t, l, "a.jpg")
	q := testJob(t, l, "b.jpg")
	postTest(t, s, "/api/curation/"+p.ID, `{"location":"Zurich"}`)
	postTest(t, s, "/api/curation/"+q.ID, `{"location":"Amsterdam"}`)
	for _, tc := range []struct{ direction, first, second string }{{"asc", q.ID, p.ID}, {"desc", p.ID, q.ID}} {
		for offset, want := range []string{tc.first, tc.second} {
			var result struct{ Items []Photo }
			path := "/api/search?mode=metadata&sort=location&direction=" + tc.direction + "&limit=1&offset=" + string(rune('0'+offset))
			if status := getJSON(t, s, path, &result); status != 200 || len(result.Items) != 1 || result.Items[0].ID != want {
				t.Fatalf("%s: status %d items %+v", path, status, result.Items)
			}
		}
	}
	for _, sort := range []string{"filename", "taken_at", "created_at", "size", "person", "people_count"} {
		var result struct{ Items []Photo }
		if getJSON(t, s, "/api/search?sort="+sort, &result) != 200 || len(result.Items) == 0 {
			t.Fatalf("sort %s failed", sort)
		}
	}
	for _, query := range []string{"sort=" + url.QueryEscape("path; DROP TABLE images"), "direction=sideways"} {
		var result map[string]any
		if getJSON(t, s, "/api/search?"+query, &result) != 400 {
			t.Fatalf("accepted invalid sort %s", query)
		}
	}
}

func TestFilteredPhotoPagination(t *testing.T) {
	l, s, _ := fixture(t)
	first := testJob(t, l, "a.jpg")
	second := testJob(t, l, "b.jpg")
	for _, j := range []aiJob{first, second} {
		if err := l.ai.save(context.Background(), j, syntheticPrediction()); err != nil {
			t.Fatal(err)
		}
		if postTest(t, s, "/api/curation/"+j.ID, `{"tags":["Family"]}`) != 200 {
			t.Fatal("tag")
		}
	}
	var person int64
	if err := l.ai.read.QueryRow("SELECT person_id FROM faces WHERE image_id=?", first.ID).Scan(&person); err != nil {
		t.Fatal(err)
	}
	for _, filter := range []string{"tag=family", "person=" + fmt.Sprint(person)} {
		for offset, want := range []string{first.ID, second.ID} {
			var result struct {
				Items []Photo
				Total int
			}
			path := "/api/search?sort=filename&limit=1&offset=" + fmt.Sprint(offset) + "&" + filter
			if getJSON(t, s, path, &result) != 200 || result.Total != 2 || len(result.Items) != 1 || result.Items[0].ID != want {
				t.Fatal(path, result)
			}
		}
	}
	l.db.Exec("UPDATE images SET signature='changed' WHERE id=?", first.ID)
	var result struct {
		Items []Photo
		Total int
	}
	if getJSON(t, s, "/api/search?person="+fmt.Sprint(person), &result) != 200 || result.Total != 1 || result.Items[0].ID != second.ID {
		t.Fatal("stale person match", result)
	}
}
