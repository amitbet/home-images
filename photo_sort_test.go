package main

import (
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
