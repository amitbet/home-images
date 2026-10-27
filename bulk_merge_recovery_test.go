package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"testing"
)

func TestBulkMergeSkipsChangedGroupsAndPreservesOthers(t *testing.T) {
	l, s, _ := fixture(t)
	ctx := context.Background()
	j := testJob(t, l, "a.jpg")
	if err := l.ai.save(ctx, j, syntheticPrediction()); err != nil {
		t.Fatal(err)
	}
	var target int64
	l.ai.db.QueryRow("SELECT person_id FROM faces WHERE image_id=?", j.ID).Scan(&target)
	add := func(name string) int64 {
		t.Helper()
		result, err := l.ai.db.Exec("INSERT INTO people(name,model,embedding) VALUES(?,?,zeroblob(12))", name, l.config.ML.FaceModel)
		if err != nil {
			t.Fatal(err)
		}
		id, _ := result.LastInsertId()
		return id
	}
	already, elsewhere, otherTarget, valid := add("Already done"), add("Changed group"), add("Other person"), add("Valid group")
	l.ai.db.Exec("UPDATE people SET merged_into=? WHERE id=?", target, already)
	l.ai.db.Exec("UPDATE people SET merged_into=? WHERE id=?", otherTarget, elsewhere)
	type outcome struct {
		Merged    int
		MergedIDs []int64 `json:"merged_ids"`
		Already   int     `json:"already_merged"`
		Failed    []mergeFailure
	}
	call := func(ids []int64) outcome {
		t.Helper()
		raw, _ := json.Marshal(map[string]any{"sources": ids})
		req, _ := http.NewRequest("POST", fmt.Sprintf("%s/api/people/%d/merge-many", s.URL, target), bytes.NewReader(raw))
		req.Header.Set("X-Home-Images", "1")
		req.Header.Set("Content-Type", "application/json")
		resp, err := s.Client().Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		var result outcome
		if err = json.NewDecoder(resp.Body).Decode(&result); err != nil || resp.StatusCode != 200 {
			t.Fatal(resp.StatusCode, err)
		}
		return result
	}
	result := call([]int64{already, elsewhere, 999999, valid})
	if result.Merged != 1 || result.Already != 1 || len(result.Failed) != 2 || len(result.MergedIDs) != 1 || result.MergedIDs[0] != valid {
		t.Fatal(result)
	}
	if result.Failed[0].ID != elsewhere || result.Failed[0].CurrentPerson != otherTarget || result.Failed[1].ID != 999999 {
		t.Fatal(result.Failed)
	}
	var parent int64
	l.ai.db.QueryRow("SELECT merged_into FROM people WHERE id=?", elsewhere).Scan(&parent)
	if parent != otherTarget {
		t.Fatal("stole another person's group")
	}
	result = call([]int64{already, elsewhere, 999999, valid})
	if result.Merged != 0 || result.Already != 2 || len(result.Failed) != 2 {
		t.Fatal("retry", result)
	}
	if postTest(t, s, "/api/people/undo", fmt.Sprintf(`{"source":%d,"target":%d}`, valid, target)) != 200 {
		t.Fatal("retry erased successful undo")
	}
}

func TestBulkMergeDatabaseFailureRollsBackValidGroups(t *testing.T) {
	l, s, _ := fixture(t)
	var ids []int64
	for n := 0; n < 3; n++ {
		result, err := l.ai.db.Exec("INSERT INTO people(model,embedding) VALUES(?,zeroblob(12))", l.config.ML.FaceModel)
		if err != nil {
			t.Fatal(err)
		}
		id, _ := result.LastInsertId()
		ids = append(ids, id)
	}
	if _, err := l.ai.db.Exec(fmt.Sprintf(`CREATE TRIGGER simulated_failure BEFORE UPDATE OF merged_into ON people WHEN old.id=%d BEGIN SELECT RAISE(ABORT,'simulated storage failure');END;`, ids[2])); err != nil {
		t.Fatal(err)
	}
	if status := postTest(t, s, fmt.Sprintf("/api/people/%d/merge-many", ids[0]), fmt.Sprintf(`{"sources":[%d,%d]}`, ids[1], ids[2])); status != 500 {
		t.Fatal(status)
	}
	var changed int
	l.ai.db.QueryRow("SELECT count(*) FROM people WHERE merged_into IS NOT NULL").Scan(&changed)
	if changed != 0 {
		t.Fatal("database error left partial changes", changed)
	}
}
