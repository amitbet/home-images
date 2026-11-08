package main

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"testing"
)

func TestSimilarPhotosWithoutMLWorker(t *testing.T) {
	l, s, _ := fixture(t)
	l.config.ML.URL = ""
	l.config.ML.TextModelDirectory = t.TempDir()
	l.ai.ml.http.Transport = mlRoundTripFunc(func(*http.Request) (*http.Response, error) {
		t.Error("image similarity contacted ML worker")
		return nil, context.Canceled
	})
	paths := []string{"a.jpg", "b.jpg", "album_100%/c.jpg", "albumX100other/d.jpg"}
	clips := [][]float32{{1, 0, 0}, {.9, .1, 0}, {.1, .9, 0}, {1, 0, 0}}
	jobs := []aiJob{}
	for i, path := range paths {
		job := testJob(t, l, path)
		jobs = append(jobs, job)
		pred := syntheticPrediction()
		pred.Clip, _ = json.Marshal(clips[i])
		if i == 1 {
			face := pred.Faces[0]
			face.Embedding = json.RawMessage(`"[0,1,0]"`)
			pred.Faces = append(pred.Faces, face)
		}
		if i == 3 {
			pred.Faces = nil
		}
		if err := l.ai.save(context.Background(), job, pred); err != nil {
			t.Fatal(err)
		}
	}
	var person, second int64
	if err := l.ai.db.QueryRow("SELECT person_id FROM faces WHERE image_id=?", jobs[0].ID).Scan(&person); err != nil {
		t.Fatal(err)
	}
	if err := l.ai.db.QueryRow("SELECT person_id FROM faces WHERE image_id=? AND person_id!=?", jobs[1].ID, person).Scan(&second); err != nil {
		t.Fatal(err)
	}
	type result struct {
		Items []struct {
			ID         string
			Similarity float32
		}
		Semantic   bool
		Total      int
		Candidates int `json:"indexed_candidates"`
	}
	base := "/api/search?mode=semantic&similar_to=" + jobs[0].ID
	if status := postTest(t, s, "/api/collections", fmt.Sprintf(`{"name":"Similar photos","query":{"mode":"semantic","similar_to":"%s","people_filter":"[\"%d\"]","sort":"relevance"}}`, jobs[0].ID, person)); status != 201 {
		t.Fatal("could not save image selection", status)
	}
	var all result
	if status := getJSON(t, s, base+"&sort=relevance&limit=60", &all); status != 200 || !all.Semantic || all.Total != 3 || len(all.Items) != 3 || all.Items[0].ID != jobs[3].ID || all.Items[1].ID != jobs[1].ID {
		t.Fatalf("similar results: %d %+v", status, all)
	}
	for _, item := range all.Items {
		if item.ID == jobs[0].ID {
			t.Fatal("source photo included")
		}
	}
	filter := fmt.Sprintf("&people_filter=%%5B%%22%d%%22%%5D", person)
	for offset, want := range []string{jobs[2].ID, jobs[1].ID} {
		var page result
		if status := getJSON(t, s, base+filter+fmt.Sprintf("&sort=filename&direction=asc&limit=1&offset=%d", offset), &page); status != 200 || page.Total != 2 || page.Candidates != 2 || len(page.Items) != 1 || page.Items[0].ID != want {
			t.Fatalf("filtered page: %d %+v", status, page)
		}
	}
	var intersection result
	filter = fmt.Sprintf("&people_filter=%%5B%%22%d%%22,%%22%d%%22%%5D", person, second)
	if status := getJSON(t, s, base+filter, &intersection); status != 200 || len(intersection.Items) != 1 || intersection.Items[0].ID != jobs[1].ID {
		t.Fatalf("intersection: %d %+v", status, intersection)
	}
	var grouped result
	if status := getJSON(t, s, base+"&group_by=year", &grouped); status != 200 || !grouped.Semantic || len(grouped.Items) != 3 {
		t.Fatalf("grouped: %d %+v", status, grouped)
	}
	var playback struct{ ID string }
	if status := getJSON(t, s, "/api/playback/next?mode=semantic&similar_to="+jobs[0].ID+filter+"&order=sequential", &playback); status != 200 || playback.ID != jobs[1].ID {
		t.Fatalf("playback: %d %+v", status, playback)
	}
	for _, suffix := range []string{"&mode=metadata", "&q=tree"} {
		// Use Set-equivalent explicit query construction for conflicting mode.
		path := base + suffix
		if suffix == "&mode=metadata" {
			path = "/api/search?mode=metadata&similar_to=" + jobs[0].ID
		}
		if status := getJSON(t, s, path, nil); status != 400 {
			t.Fatalf("accepted invalid image query %s: %d", path, status)
		}
	}
	if status := getJSON(t, s, "/api/search?similar_to=invalid", nil); status != 400 {
		t.Fatal(status)
	}
	if status := getJSON(t, s, "/api/search?similar_to=000000000000000000000000", nil); status != 404 {
		t.Fatal(status)
	}
	if _, err := l.db.Exec("UPDATE images SET signature='changed' WHERE id=?", jobs[0].ID); err != nil {
		t.Fatal(err)
	}
	if status := getJSON(t, s, base, nil); status != 404 {
		t.Fatalf("stale source accepted: %d", status)
	}
}
