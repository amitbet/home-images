package main

import (
	"context"
	"testing"
)

func TestPersonCaptureYears(t *testing.T) {
	l, s, _ := fixture(t)
	a := testJob(t, l, "a.jpg")
	b := testJob(t, l, "b.jpg")
	c := testJob(t, l, "album_100%/c.jpg")
	prediction := syntheticPrediction()
	for _, j := range []aiJob{a, b} {
		if err := l.ai.save(context.Background(), j, prediction); err != nil {
			t.Fatal(err)
		}
	}
	prediction.Faces[0].Embedding = []byte(`"[0,1,0]"`)
	if err := l.ai.save(context.Background(), c, prediction); err != nil {
		t.Fatal(err)
	}
	var group, target int64
	if err := l.ai.db.QueryRow("SELECT person_id FROM faces WHERE image_id=?", a.ID).Scan(&group); err != nil {
		t.Fatal(err)
	}
	if err := l.ai.db.QueryRow("SELECT person_id FROM faces WHERE image_id=?", c.ID).Scan(&target); err != nil {
		t.Fatal(err)
	}
	for id, date := range map[string]string{a.ID: "2015-06-01T00:00:00Z", b.ID: "2022-08-10T00:00:00Z", c.ID: "-000-01-01"} {
		if _, err := l.db.Exec("UPDATE images SET taken_at=? WHERE id=?", date, id); err != nil {
			t.Fatal(err)
		}
	}
	check := func(first, last int) {
		t.Helper()
		var result struct{ Items []personSummary }
		if getJSON(t, s, "/api/people", &result) != 200 {
			t.Fatal("people request failed")
		}
		found := false
		for _, p := range result.Items {
			if p.ID == group {
				found = true
				if p.FirstYear != first || p.LastYear != last {
					t.Fatalf("people years: %+v", p)
				}
			}
			if p.ID == target && (p.FirstYear != 0 || p.LastYear != 0) {
				t.Fatalf("invalid capture year: %+v", p)
			}
		}
		if !found {
			t.Fatal("group missing")
		}
		var matches struct{ Items []personSuggestion }
		if getJSON(t, s, "/api/people/"+fmtID(target)+"/suggestions?min_score=0", &matches) != 200 || len(matches.Items) != 1 {
			t.Fatalf("suggestions: %+v", matches)
		}
		p := matches.Items[0]
		if p.ID != group || p.FirstYear != first || p.LastYear != last {
			t.Fatalf("suggestion years: %+v", p)
		}
	}
	check(2015, 2022)
	if postTest(t, s, "/api/curation/"+b.ID, `{"hidden":true}`) != 200 {
		t.Fatal("hide failed")
	}
	check(2015, 2015)
	if postTest(t, s, "/api/curation/"+b.ID, `{"hidden":false}`) != 200 {
		t.Fatal("unhide failed")
	}
	if _, err := l.db.Exec("UPDATE images SET signature='changed' WHERE id=?", b.ID); err != nil {
		t.Fatal(err)
	}
	check(2015, 2015)
	if _, err := l.db.Exec("UPDATE images SET taken_at='' WHERE id=?", a.ID); err != nil {
		t.Fatal(err)
	}
	check(0, 0)
}
