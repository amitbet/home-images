package main

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"testing"
)

func TestGroupedSearchMembershipsAndPagination(t *testing.T) {
	l, s, _ := fixture(t)
	a := testJob(t, l, "a.jpg")
	b := testJob(t, l, "b.jpg")
	if _, e := l.db.Exec("DELETE FROM images WHERE id NOT IN(?,?)", a.ID, b.ID); e != nil {
		t.Fatal(e)
	}
	l.db.Exec("UPDATE images SET taken_at='2020-01-01' WHERE id=?", a.ID)
	l.db.Exec("UPDATE images SET taken_at='2021-01-01' WHERE id=?", b.ID)
	postTest(t, s, "/api/curation/"+a.ID, `{"tags":["Family","Holiday"]}`)
	postTest(t, s, "/api/curation/"+b.ID, `{"tags":["Family"]}`)
	pred := syntheticPrediction()
	other := pred.Faces[0]
	other.Embedding = json.RawMessage(`"[0,1,0]"`)
	pred.Faces = append(pred.Faces, other)
	if e := l.ai.save(context.Background(), a, pred); e != nil {
		t.Fatal(e)
	}
	if e := l.ai.save(context.Background(), b, syntheticPrediction()); e != nil {
		t.Fatal(e)
	}
	type response struct {
		Items  []groupedPhoto
		Total  int
		Unique int `json:"unique_total"`
	}
	for _, kind := range []string{"path", "year", "people", "tag"} {
		var all response
		if getJSON(t, s, "/api/search?group_by="+kind, &all) != 200 {
			t.Fatal(kind)
		}
		want := 2
		if kind == "people" || kind == "tag" {
			want = 3
		}
		if all.Total != want || all.Unique != 2 || len(all.Items) != want {
			t.Fatal(kind, all)
		}
		for offset, p := range all.Items {
			var page response
			if getJSON(t, s, fmt.Sprintf("/api/search?group_by=%s&limit=1&offset=%d", kind, offset), &page) != 200 || len(page.Items) != 1 || page.Items[0].ID != p.ID || page.Items[0].GroupKey != p.GroupKey {
				t.Fatal(kind, offset, page)
			}
		}
	}
	postTest(t, s, "/api/curation/"+b.ID, `{"hidden":true}`)
	var hidden response
	getJSON(t, s, "/api/search?group_by=tag", &hidden)
	if hidden.Total != 2 || hidden.Unique != 1 {
		t.Fatal("hidden memberships", hidden)
	}
	for _, query := range []string{"group_by=invalid", "group_by=invalid&mode=semantic&q=cat"} {
		var result map[string]any
		if getJSON(t, s, "/api/search?"+query, &result) != 400 {
			t.Fatal(query)
		}
	}
}

func TestFilterAutocompleteAndAutoPlaces(t *testing.T) {
	l, s, _ := fixture(t)
	a := testJob(t, l, "a.jpg")
	b := testJob(t, l, "b.jpg")
	postTest(t, s, "/api/curation/"+a.ID, `{"tags":["Family","100%"],"location":"Rome"}`)
	postTest(t, s, "/api/curation/"+b.ID, `{"tags":["Secret"],"hidden":true}`)
	if _, e := l.db.Exec("INSERT INTO locations VALUES(?,?,?)", a.ID, a.Signature, "Milan, Italy"); e != nil {
		t.Fatal(e)
	}
	c := testJob(t, l, "album_100%/c.jpg")
	if _, e := l.db.Exec("INSERT INTO locations VALUES(?,?,?)", c.ID, c.Signature, "Florence, Italy"); e != nil {
		t.Fatal(e)
	}
	for _, tc := range []struct{ kind, q, want string }{{"tag", "fam", "Family"}, {"tag", "%", "100%"}, {"folder", "100%", "album_100%"}, {"place", "Flor", "Florence, Italy"}, {"place", "Rom", "Rome"}} {
		var result struct{ Items []string }
		if getJSON(t, s, "/api/filter-suggestions?kind="+tc.kind+"&q="+url.QueryEscape(tc.q), &result) != 200 || len(result.Items) != 1 || result.Items[0] != tc.want {
			t.Fatal(tc, result)
		}
	}
	var suggestions struct{ Items []string }
	getJSON(t, s, "/api/filter-suggestions?kind=tag&q=Secret", &suggestions)
	if len(suggestions.Items) != 0 {
		t.Fatal("hidden tag exposed")
	}
	var photos struct {
		Items []Photo
		Total int
	}
	getJSON(t, s, "/api/search?place=Florence", &photos)
	if photos.Total != 1 || photos.Items[0].ID != c.ID {
		t.Fatal("automatic location not searchable", photos)
	}
	getJSON(t, s, "/api/search?place=Milan", &photos)
	if photos.Total != 0 {
		t.Fatal("manual override ignored", photos)
	}
}

func TestGroupedUnassignedPaging(t *testing.T) {
	l, s, _ := fixture(t)
	a := testJob(t, l, "a.jpg")
	if e := l.ai.save(context.Background(), a, syntheticPrediction()); e != nil {
		t.Fatal(e)
	}
	postTest(t, s, "/api/curation/"+a.ID, `{"tags":["Family","Holiday"],"location":"Rome"}`)
	for _, kind := range []string{"people", "tag"} {
		var all struct {
			Items []groupedPhoto
			Total int
		}
		getJSON(t, s, "/api/search?group_by="+kind+"&sort=location", &all)
		for offset, p := range all.Items {
			var page struct {
				Items []groupedPhoto
				Total int
			}
			if getJSON(t, s, fmt.Sprintf("/api/search?group_by=%s&sort=location&limit=1&offset=%d", kind, offset), &page) != 200 || len(page.Items) != 1 || page.Items[0].ID != p.ID || page.Items[0].GroupKey != p.GroupKey || page.Total != all.Total {
				t.Fatal(kind, offset, page)
			}
		}
	}
}

func TestYearGroupsFollowDateSortDirection(t *testing.T) {
	l, s, _ := fixture(t)
	first := testJob(t, l, "a.jpg")
	second := testJob(t, l, "b.jpg")
	unknown := testJob(t, l, "album_100%/c.jpg")
	l.db.Exec("DELETE FROM images WHERE id NOT IN(?,?,?)", first.ID, second.ID, unknown.ID)
	l.db.Exec("UPDATE images SET taken_at='2020-06-01',created_at=NULL WHERE id=?", first.ID)
	l.db.Exec("UPDATE images SET taken_at='2024-06-01',created_at=NULL WHERE id=?", second.ID)
	l.db.Exec("UPDATE images SET taken_at=NULL,created_at=NULL WHERE id=?", unknown.ID)
	for _, tc := range []struct {
		direction string
		ids       []string
	}{{"asc", []string{first.ID, second.ID, unknown.ID}}, {"desc", []string{second.ID, first.ID, unknown.ID}}, {"asc", []string{first.ID, second.ID, unknown.ID}}} {
		for offset, want := range tc.ids {
			var result struct {
				Items []groupedPhoto
				Total int
			}
			path := fmt.Sprintf("/api/search?group_by=year&sort=taken_at&direction=%s&limit=1&offset=%d", tc.direction, offset)
			if getJSON(t, s, path, &result) != 200 || result.Total != 3 || len(result.Items) != 1 || result.Items[0].ID != want {
				t.Fatal(path, result)
			}
		}
	}
}

func TestAllGroupingSortDirectionsAndPaging(t *testing.T) {
	l, s, _ := fixture(t)
	first := testJob(t, l, "a.jpg")
	second := testJob(t, l, "b.jpg")
	third := testJob(t, l, "album_100%/c.jpg")
	l.db.Exec("DELETE FROM images WHERE id NOT IN(?,?,?)", first.ID, second.ID, third.ID)
	l.db.Exec("UPDATE images SET directory='Alpha',taken_at='2020-01-01',created_at='2021-01-01',size_bytes=100 WHERE id=?", first.ID)
	l.db.Exec("UPDATE images SET directory='Zulu',taken_at='2024-01-01',created_at='2025-01-01',size_bytes=300 WHERE id=?", second.ID)
	l.db.Exec("UPDATE images SET directory='Alpha',taken_at='2022-01-01',created_at='2023-01-01',size_bytes=200 WHERE id=?", third.ID)
	postTest(t, s, "/api/curation/"+first.ID, `{"tags":["Alpha","Zulu"],"location":"Alpha"}`)
	postTest(t, s, "/api/curation/"+second.ID, `{"tags":["Zulu"],"location":"Zulu"}`)
	postTest(t, s, "/api/curation/"+third.ID, `{"location":"Middle"}`)
	for n, j := range []aiJob{first, second} {
		pred := syntheticPrediction()
		if n == 1 {
			pred.Faces[0].Embedding = json.RawMessage(`"[0,1,0]"`)
		}
		if err := l.ai.save(context.Background(), j, pred); err != nil {
			t.Fatal(err)
		}
	}
	l.ai.db.Exec("UPDATE people SET name=CASE WHEN id=(SELECT min(id) FROM people) THEN 'Alpha' ELSE 'Zulu' END")
	type response struct {
		Items []groupedPhoto
		Total int
	}
	for _, kind := range []string{"path", "year", "people", "tag"} {
		for _, sortKey := range []string{"filename", "taken_at", "created_at", "size", "person", "people_count", "location"} {
			// Repeat asc after desc to exercise direction changes against the group cache.
			for _, direction := range []string{"asc", "desc", "asc"} {
				params := "sort=" + sortKey + "&direction=" + direction
				var flat struct{ Items []Photo }
				if getJSON(t, s, "/api/search?"+params, &flat) != 200 {
					t.Fatal(params)
				}
				ranks := map[string]int{}
				for n, p := range flat.Items {
					ranks[p.ID] = n
				}
				var all response
				url := "/api/search?group_by=" + kind + "&" + params
				if getJSON(t, s, url, &all) != 200 || len(all.Items) != all.Total {
					t.Fatal(url, all)
				}
				previousGroup, previousRank := "", -1
				for n, p := range all.Items {
					group := foldGroupLabel(p.GroupLabel)
					if kind == "path" || kind == "year" {
						group = foldGroupLabel(p.GroupKey)
					}
					fallback := (kind == "people" && p.GroupKey == "none") || (kind == "tag" && p.GroupKey == "")
					if n > 0 && group != previousGroup && !fallback {
						if (direction == "asc" && group < previousGroup) || (direction == "desc" && group > previousGroup) {
							t.Fatal("group order", url, all.Items)
						}
					}
					if group == previousGroup && ranks[p.ID] < previousRank {
						t.Fatal("photo order", url, all.Items)
					}
					previousGroup, previousRank = group, ranks[p.ID]
					var page response
					if getJSON(t, s, fmt.Sprintf("%s&limit=1&offset=%d", url, n), &page) != 200 || page.Total != all.Total || len(page.Items) != 1 || page.Items[0].ID != p.ID || page.Items[0].GroupKey != p.GroupKey {
						t.Fatal("pagination", url, n, page)
					}
				}
			}
		}
	}
}
