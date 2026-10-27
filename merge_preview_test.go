package main

import (
	"context"
	"encoding/json"
	"fmt"
	"testing"
)

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
	url := fmt.Sprintf("/api/people/999999/merge-preview?sources=%d,%d", ids[0], ids[1])
	var result struct{ Groups, Images int }
	if getJSON(t, s, url, &result) != 200 || result.Groups != 2 || result.Images != 1 {
		t.Fatal(result)
	}
	postTest(t, s, "/api/curation/"+j.ID, `{"hidden":true}`)
	if getJSON(t, s, url, &result) != 200 || result.Images != 0 {
		t.Fatal("hidden photo counted", result)
	}
	if getJSON(t, s, url+"&hidden=all", &result) != 200 || result.Images != 1 {
		t.Fatal("explicit hidden filter", result)
	}
	if _, err = l.db.Exec("UPDATE images SET signature='changed' WHERE id=?", j.ID); err != nil {
		t.Fatal(err)
	}
	if getJSON(t, s, url+"&hidden=all", &result) != 200 || result.Images != 0 {
		t.Fatal("stale face counted", result)
	}
}
