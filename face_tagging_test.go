package main

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"
)

func TestTagGroupResolvesExistingNameInSave(t *testing.T) {
	l, s, _ := fixture(t)
	ctx := context.Background()
	for n, path := range []string{"a.jpg", "b.jpg"} {
		prediction := syntheticPrediction()
		if n == 1 {
			prediction.Faces[0].Embedding = json.RawMessage(`"[0,1,0]"`)
		}
		if err := l.ai.save(ctx, testJob(t, l, path), prediction); err != nil {
			t.Fatal(err)
		}
	}
	var target, source int64
	if err := l.ai.db.QueryRow("SELECT person_id FROM faces WHERE image_id=?", imageID("a.jpg")).Scan(&target); err != nil {
		t.Fatal(err)
	}
	if err := l.ai.db.QueryRow("SELECT person_id FROM faces WHERE image_id=?", imageID("b.jpg")).Scan(&source); err != nil {
		t.Fatal(err)
	}
	if postTest(t, s, "/api/people/"+fmtID(target), `{"name":"Dana"}`) != 200 {
		t.Fatal("rename failed")
	}
	// No suggestion lookup is required, including when case/spacing differs.
	req, _ := http.NewRequest("POST", s.URL+"/api/people/"+fmtID(source), strings.NewReader(`{"name":" dana ","match_existing":true}`))
	req.Header.Set("X-Home-Images", "1")
	resp, err := s.Client().Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	var result struct {
		MergedInto int64  `json:"merged_into"`
		Name       string `json:"name"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil || resp.StatusCode != 200 || result.MergedInto != target || result.Name != "Dana" {
		t.Fatal(resp.StatusCode, result, err)
	}
	var count int
	if err := l.ai.db.QueryRow("SELECT count(*) FROM faces WHERE person_id=?", target).Scan(&count); err != nil || count != 2 {
		t.Fatal(count, err)
	}
	// The normal merge undo continues to work after this shorter tagging path.
	if postTest(t, s, "/api/people/undo", `{"source":`+fmtID(source)+`,"target":`+fmtID(target)+`}`) != 200 {
		t.Fatal("tag merge could not be undone")
	}
	if postTest(t, s, "/api/people/"+fmtID(source), `{"name":"Robin","match_existing":true}`) != 200 {
		t.Fatal("new name failed")
	}
	var name string
	if err := l.ai.db.QueryRow("SELECT name FROM people WHERE id=? AND merged_into IS NULL", source).Scan(&name); err != nil || name != "Robin" {
		t.Fatal(name, err)
	}
}
