package main

import (
	"context"
	"testing"
)

func TestPersonSuggestionsAndDismissal(t *testing.T) {
	l, s, _ := fixture(t)
	a := l.ai
	prediction := syntheticPrediction()
	if err := a.save(context.Background(), testJob(t, l, "a.jpg"), prediction); err != nil {
		t.Fatal(err)
	}
	prediction.Faces[0].Embedding = []byte(`"[0.5,0.866,0]"`)
	if err := a.save(context.Background(), testJob(t, l, "b.jpg"), prediction); err != nil {
		t.Fatal(err)
	}
	var ids []int64
	rows, err := a.db.Query("SELECT id FROM people ORDER BY id")
	if err != nil {
		t.Fatal(err)
	}
	for rows.Next() {
		var id int64
		rows.Scan(&id)
		ids = append(ids, id)
	}
	rows.Close()
	if len(ids) != 2 {
		t.Fatalf("expected two groups: %v", ids)
	}
	var result struct{ Items []personSuggestion }
	path := "/api/people/" + fmtID(ids[0])
	if getJSON(t, s, path+"/suggestions", &result) != 200 || len(result.Items) != 1 || result.Items[0].ID != ids[1] {
		t.Fatalf("suggestions: %+v", result)
	}
	if postTest(t, s, path+"/dismiss", `{"other":`+fmtID(ids[1])+`}`) != 200 {
		t.Fatal("dismiss failed")
	}
	getJSON(t, s, path+"/suggestions", &result)
	if len(result.Items) != 0 {
		t.Fatal("dismissed group returned")
	}
	getJSON(t, s, "/api/people/"+fmtID(ids[1])+"/suggestions", &result)
	if len(result.Items) != 0 {
		t.Fatal("dismissal must be symmetric")
	}
	if postTest(t, s, path+"/dismiss", `{"other":`+fmtID(ids[0])+`}`) != 400 {
		t.Fatal("self dismissal accepted")
	}
	// An explicit user-confirmed merge remains possible after dismissal.
	if postTest(t, s, path, `{"merge_into":`+fmtID(ids[1])+`}`) != 200 {
		t.Fatal("merge failed")
	}
	var count int
	a.db.QueryRow("SELECT count(*) FROM people WHERE coalesce(merged_into,id)=?", ids[1]).Scan(&count)
	if count != 2 {
		t.Fatal("merge lost retained representative")
	}
	var history struct {
		Available      bool
		Source, Target int64
	}
	if getJSON(t, s, "/api/people/merge-history", &history) != 200 || !history.Available {
		t.Fatal("undo missing")
	}
	if postTest(t, s, "/api/people/undo", `{"source":`+fmtID(ids[0])+`,"target":`+fmtID(ids[1])+`}`) != 200 {
		t.Fatal("undo failed")
	}
	a.db.QueryRow("SELECT count(*) FROM people WHERE merged_into IS NULL").Scan(&count)
	if count != 2 {
		t.Fatal("undo did not restore groups")
	}
	getJSON(t, s, path+"/suggestions", &result)
	if len(result.Items) != 0 {
		t.Fatal("undo lost dismissal")
	}
	var face int64
	a.db.QueryRow("SELECT id FROM faces WHERE person_id=?", ids[0]).Scan(&face)
	if postTest(t, s, "/api/faces/"+fmtID(face), `{"person":`+fmtID(ids[1])+`}`) != 200 {
		t.Fatal("move failed")
	}
	if err := a.save(context.Background(), testJob(t, l, "a.jpg"), syntheticPrediction()); err != nil {
		t.Fatal(err)
	}
	var assigned int64
	a.db.QueryRow("SELECT person_id FROM faces WHERE image_id=?", testJob(t, l, "a.jpg").ID).Scan(&assigned)
	if assigned != ids[1] {
		t.Fatal("reindex lost face correction")
	}

}

func TestBulkMergeAtomicAndUndo(t *testing.T) {
	l, s, _ := fixture(t)
	var ids []int64
	for _, name := range []string{"Keep", "First", "Second"} {
		result, err := l.ai.db.Exec("INSERT INTO people(name,model,embedding) VALUES(?,?,?)", name, l.config.ML.FaceModel, []byte{0, 0, 128, 63})
		if err != nil {
			t.Fatal(err)
		}
		id, _ := result.LastInsertId()
		ids = append(ids, id)
	}
	path := "/api/people/" + fmtID(ids[0]) + "/merge-many"
	if code := postTest(t, s, path, `{"sources":[`+fmtID(ids[1])+`,999999]}`); code != 404 {
		t.Fatal("expected stale selection rejection", code)
	}
	var count int
	l.ai.db.QueryRow("SELECT count(*) FROM people WHERE merged_into IS NOT NULL").Scan(&count)
	if count != 0 {
		t.Fatal("partial merge committed")
	}
	if code := postTest(t, s, path, `{"sources":[`+fmtID(ids[1])+`,`+fmtID(ids[2])+`]}`); code != 200 {
		t.Fatal("bulk merge", code)
	}
	l.ai.db.QueryRow("SELECT count(*) FROM people WHERE merged_into=?", ids[0]).Scan(&count)
	if count != 2 {
		t.Fatal("selected groups not merged", count)
	}
	for _, source := range []int64{ids[2], ids[1]} {
		if code := postTest(t, s, "/api/people/undo", `{"source":`+fmtID(source)+`,"target":`+fmtID(ids[0])+`}`); code != 200 {
			t.Fatal("bulk undo", code)
		}
	}
	l.ai.db.QueryRow("SELECT count(*) FROM people WHERE merged_into IS NULL").Scan(&count)
	if count != 3 {
		t.Fatal("undo did not restore all groups", count)
	}
}
