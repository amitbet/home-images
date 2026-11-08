package main

import (
	"context"
	"encoding/json"
	"fmt"
	"testing"
)

func TestSemanticSortBeforePagination(t *testing.T) {
	l, s, _ := fixture(t)
	jobs := []aiJob{testJob(t, l, "a.jpg"), testJob(t, l, "b.jpg"), testJob(t, l, "album_100%/c.jpg")}
	for index, job := range jobs {
		pred := syntheticPrediction()
		// Similarity order is a,b,c; dates intentionally put b first, then c,a.
		pred.Clip, _ = json.Marshal([]float32{float32(3 - index), float32(index), 0})
		if index == 2 {
			pred.Faces = nil
		}
		if err := l.ai.save(context.Background(), job, pred); err != nil {
			t.Fatal(err)
		}
		date := []string{"2010-01-01", "2026-01-01", "2020-01-01"}[index]
		if _, err := l.db.Exec("UPDATE images SET taken_at=? WHERE id=?", date, job.ID); err != nil {
			t.Fatal(err)
		}
	}
	l.ai.textCache["throwing kids up to the sky"] = vector{1, 0, 0}
	var person int64
	if err := l.ai.db.QueryRow("SELECT person_id FROM faces WHERE image_id=?", jobs[0].ID).Scan(&person); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		sort, direction string
		want            []string
	}{
		{"taken_at", "desc", []string{jobs[1].ID, jobs[0].ID}},
		{"taken_at", "asc", []string{jobs[0].ID, jobs[1].ID}},
		{"filename", "desc", []string{jobs[1].ID, jobs[0].ID}},
		{"relevance", "desc", []string{jobs[0].ID, jobs[1].ID}},
	} {
		for offset, want := range tc.want {
			var result struct {
				Items []struct {
					ID         string
					Similarity float32
				}
				Semantic   bool
				Total      int
				Candidates int `json:"indexed_candidates"`
			}
			path := fmt.Sprintf("/api/search?mode=semantic&q=throwing+kids+up+to+the+sky&people_filter=%%5B%%22%d%%22%%5D&sort=%s&direction=%s&limit=1&offset=%d", person, tc.sort, tc.direction, offset)
			if status := getJSON(t, s, path, &result); status != 200 || !result.Semantic || result.Total != 2 || result.Candidates != 2 || len(result.Items) != 1 || result.Items[0].ID != want || result.Items[0].Similarity <= 0 {
				t.Fatalf("%s: status=%d result=%+v", path, status, result)
			}
		}
	}
	// Sorting must use all semantic candidates, not merely the first ranked page.
	var all struct{ Items []Photo }
	if status := getJSON(t, s, "/api/search?mode=semantic&q=throwing+kids+up+to+the+sky&sort=taken_at&direction=desc&limit=1&offset=1", &all); status != 200 || len(all.Items) != 1 || all.Items[0].ID != jobs[2].ID {
		t.Fatalf("unfiltered second page: %d %+v", status, all)
	}
}

// Scrolling pages through one ranked snapshot instead of rescanning vectors.
func TestSemanticPagesReuseRanking(t *testing.T) {
	l, s, _ := fixture(t)
	jobs := []aiJob{testJob(t, l, "a.jpg"), testJob(t, l, "b.jpg"), testJob(t, l, "album_100%/c.jpg")}
	for index, job := range jobs {
		pred := syntheticPrediction()
		pred.Clip, _ = json.Marshal([]float32{float32(3 - index), float32(index), 0})
		if err := l.ai.save(context.Background(), job, pred); err != nil {
			t.Fatal(err)
		}
	}
	l.ai.textCache["paging"] = vector{1, 0, 0}
	page := func(offset int) []string {
		var result struct{ Items []Photo }
		path := fmt.Sprintf("/api/search?mode=semantic&q=paging&limit=2&offset=%d", offset)
		if status := getJSON(t, s, path, &result); status != 200 {
			t.Fatalf("%s: status=%d", path, status)
		}
		ids := []string{}
		for _, item := range result.Items {
			ids = append(ids, item.ID)
		}
		return ids
	}
	if got := page(0); len(got) != 2 || got[0] != jobs[0].ID || got[1] != jobs[1].ID {
		t.Fatalf("first page: %v", got)
	}
	// Without the cached embedding, a fresh ranking would have to encode the query.
	l.ai.mu.Lock()
	delete(l.ai.textCache, "paging")
	l.ai.mu.Unlock()
	if got := page(2); len(got) != 1 || got[0] != jobs[2].ID {
		t.Fatalf("second page: %v", got)
	}
}
