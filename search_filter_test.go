package main

import (
	"context"
	"testing"
)

func TestPersonFilterKeepsOnlyCurrentSuccessfulImages(t *testing.T) {
	l, s, _ := fixture(t)
	a := l.ai
	for _, path := range []string{"a.jpg", "b.jpg", "album_100%/c.jpg", "albumX100other/d.jpg"} {
		if err := a.save(context.Background(), testJob(t, l, path), syntheticPrediction()); err != nil {
			t.Fatal(err)
		}
	}
	valid := testJob(t, l, "a.jpg").ID
	var person int64
	if err := a.db.QueryRow("SELECT person_id FROM faces WHERE image_id=?", valid).Scan(&person); err != nil {
		t.Fatal(err)
	}
	// Multiple faces assigned to one person must not duplicate the photo.
	if _, err := a.db.Exec("INSERT INTO faces(image_id,person_id,box,score,width,height) SELECT image_id,person_id,box,score,width,height FROM faces WHERE image_id=?", valid); err != nil {
		t.Fatal(err)
	}
	if _, err := l.db.Exec("UPDATE images SET signature='changed' WHERE id=?", imageID("b.jpg")); err != nil {
		t.Fatal(err)
	}
	if _, err := a.db.Exec("UPDATE ai_images SET models='other-model' WHERE image_id=?", imageID("album_100%/c.jpg")); err != nil {
		t.Fatal(err)
	}
	if _, err := a.db.Exec("UPDATE ai_images SET error='failed' WHERE image_id=?", imageID("albumX100other/d.jpg")); err != nil {
		t.Fatal(err)
	}
	a.lib.config.ML.URL = "http://unused"
	a.textCache["beach"] = vector{1, 0, 0}
	for _, query := range []string{"mode=metadata", "mode=semantic&q=beach", "mode=metadata&recursive=false"} {
		var result struct {
			Items []Photo
			Total int
		}
		if code := getJSON(t, s, "/api/search?person="+fmtID(person)+"&"+query, &result); code != 200 || result.Total != 1 || len(result.Items) != 1 || result.Items[0].ID != valid {
			t.Fatalf("%s: status=%d result=%+v", query, code, result)
		}
		got := getPlayback(t, s, "person="+fmtID(person)+"&"+query)
		if got.ID != valid {
			t.Fatal("playback returned invalid person match", got)
		}
	}
	if postTest(t, s, "/api/curation/"+valid, `{"hidden":true}`) != 200 {
		t.Fatal("hide")
	}
	for _, query := range []string{"mode=metadata", "mode=semantic&q=beach"} {
		var result struct{ Total int }
		if code := getJSON(t, s, "/api/search?person="+fmtID(person)+"&"+query, &result); code != 200 || result.Total != 0 {
			t.Fatal("person filter bypassed visibility", query, code, result)
		}
	}
}
