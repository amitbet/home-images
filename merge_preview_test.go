package main

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func mergePreview(t *testing.T, s *httptest.Server, path string, sources []int64, out any) int {
	t.Helper()
	body, _ := json.Marshal(map[string]any{"sources": sources})
	req, _ := http.NewRequest("POST", s.URL+path, strings.NewReader(string(body)))
	req.Header.Set("X-Home-Images", "1")
	req.Header.Set("Content-Type", "application/json")
	resp, err := s.Client().Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if err = json.NewDecoder(resp.Body).Decode(out); err != nil {
		t.Fatal(err)
	}
	return resp.StatusCode
}

func TestMergePreviewCountsDistinctVisibleImages(t *testing.T) {
	l, s, _ := fixture(t)
	ctx := context.Background()
	j := testJob(t, l, "a.jpg")
	pred := syntheticPrediction()
	other := pred.Faces[0]
	other.Embedding = json.RawMessage(`"[0,1,0]"`)
	pred.Faces = append(pred.Faces, other)
	if err := l.ai.save(ctx, j, pred); err != nil {
		t.Fatal(err)
	}
	rows, err := l.ai.db.Query("SELECT id FROM people ORDER BY id")
	if err != nil {
		t.Fatal(err)
	}
	var ids []int64
	for rows.Next() {
		var id int64
		rows.Scan(&id)
		ids = append(ids, id)
	}
	rows.Close()
	if len(ids) != 2 {
		t.Fatal(ids)
	}
	// Target ID is not included in the source selection.
	url := "/api/people/999999/merge-preview"
	var result struct{ Groups, Images int }
	if mergePreview(t, s, url, ids, &result) != 200 || result.Groups != 2 || result.Images != 1 {
		t.Fatal(result)
	}
	postTest(t, s, "/api/curation/"+j.ID, `{"hidden":true}`)
	if mergePreview(t, s, url, ids, &result) != 200 || result.Images != 0 {
		t.Fatal("hidden photo counted", result)
	}
	if mergePreview(t, s, url+"?hidden=all", ids, &result) != 200 || result.Images != 1 {
		t.Fatal("explicit hidden filter", result)
	}
	if _, err = l.db.Exec("UPDATE images SET signature='changed' WHERE id=?", j.ID); err != nil {
		t.Fatal(err)
	}
	if mergePreview(t, s, url+"?hidden=all", ids, &result) != 200 || result.Images != 0 {
		t.Fatal("stale face counted", result)
	}
}

func TestMergeFiveThousandGroups(t *testing.T) {
	l, s, _ := fixture(t)
	j := testJob(t, l, "a.jpg")
	if err := l.ai.save(context.Background(), j, syntheticPrediction()); err != nil {
		t.Fatal(err)
	}
	var target int64
	l.ai.db.QueryRow("SELECT person_id FROM faces WHERE image_id=?", j.ID).Scan(&target)
	tx, _ := l.ai.db.Begin()
	sources := []int64{}
	for n := 0; n < maxMergeGroups; n++ {
		result, err := tx.Exec("INSERT INTO people(name,model,embedding) VALUES('',?,zeroblob(2048))", l.config.ML.FaceModel)
		if err != nil {
			t.Fatal(err)
		}
		source, _ := result.LastInsertId()
		sources = append(sources, source)
		if _, err = tx.Exec("INSERT INTO faces(image_id,person_id,box,score,width,height) VALUES(?,?,?,.9,100,100)", j.ID, source, fmt.Sprint(n)); err != nil {
			t.Fatal(err)
		}
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	var preview struct{ Groups, Images int }
	path := fmt.Sprintf("/api/people/%d/merge-preview", target)
	if code := mergePreview(t, s, path, sources, &preview); code != 200 || preview.Groups != maxMergeGroups || preview.Images != 1 {
		t.Fatal(code, preview)
	}
	var rejected map[string]string
	if mergePreview(t, s, path, append(sources, 999999999), &rejected) != 400 {
		t.Fatal("over-limit preview accepted", rejected)
	}
	body, _ := json.Marshal(map[string]any{"sources": sources})
	start := time.Now()
	if postTest(t, s, fmt.Sprintf("/api/people/%d/merge-many", target), string(body)) != 200 {
		t.Fatal("merge failed")
	}
	t.Logf("Merged %d groups in %s", maxMergeGroups, time.Since(start))
	var remaining int
	l.ai.db.QueryRow("SELECT count(*) FROM faces WHERE person_id!=?", target).Scan(&remaining)
	if remaining != 0 {
		t.Fatal("faces left on sources", remaining)
	}
	// The undo history nests one snapshot per source; it must still decode.
	var history struct {
		Available      bool
		Source, Target int64
	}
	if getJSON(t, s, "/api/people/merge-history", &history) != 200 || !history.Available || history.Source != sources[len(sources)-1] {
		t.Fatal("undo history unreadable", history)
	}
	payload, _ := json.Marshal(map[string]int64{"source": history.Source, "target": history.Target})
	if postTest(t, s, "/api/people/undo", string(payload)) != 200 {
		t.Fatal("undo failed")
	}
}
