package main

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http/httptest"
	"reflect"
	"testing"
)

func TestPeoplePaginationTotals(t *testing.T) {
	l, s, _ := fixture(t)
	for n, path := range []string{"a.jpg", "b.jpg"} {
		p := syntheticPrediction()
		if n == 1 {
			p.Faces[0].Embedding = json.RawMessage(`"[0,1,0]"`)
		}
		if err := l.ai.save(context.Background(), testJob(t, l, path), p); err != nil {
			t.Fatal(err)
		}
	}
	var previous int64
	for offset := 0; offset < 3; offset++ {
		var page struct {
			Items  []personSummary
			Total  int
			Offset int
		}
		url := "/api/people?limit=1&offset=" + strconvItoa(offset)
		if getJSON(t, s, url, &page) != 200 || page.Total != 2 || page.Offset != offset {
			t.Fatal(page)
		}
		if offset == 2 {
			if len(page.Items) != 0 {
				t.Fatal(page)
			}
			continue
		}
		if len(page.Items) != 1 || page.Items[0].ID <= previous {
			t.Fatal(page)
		}
		previous = page.Items[0].ID
	}
}

// Compare the paged plan with the original full aggregation, including duplicate
// detections, curation filters, stale indexing records, and empty pages.
func TestPeoplePageMatchesFullAggregation(t *testing.T) {
	l, s, _ := fixture(t)
	for n, path := range []string{"a.jpg", "b.jpg", "album_100%/c.jpg"} {
		p := syntheticPrediction()
		other := p.Faces[0]
		other.Embedding = []byte(`"[0,1,0]"`)
		p.Faces = append(p.Faces, other)
		if n == 2 {
			p.Faces = p.Faces[1:]
		}
		if err := l.ai.save(context.Background(), testJob(t, l, path), p); err != nil {
			t.Fatal(err)
		}
		if _, err := l.db.Exec("UPDATE images SET taken_at=? WHERE id=?", fmt.Sprintf("20%02d-06-01", n+10), imageID(path)); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := l.ai.db.Exec(`INSERT INTO faces(image_id,person_id,box,score,width,height)
 SELECT image_id,person_id,box,score,width,height FROM faces WHERE image_id=? LIMIT 1`, imageID("a.jpg")); err != nil {
		t.Fatal(err)
	}
	postTest(t, s, "/api/curation/"+imageID("a.jpg"), `{"favorite":true,"tags":["Family"],"location":"Rome"}`)
	postTest(t, s, "/api/curation/"+imageID("b.jpg"), `{"hidden":true}`)
	compare := func() {
		t.Helper()
		for _, filters := range []string{"", "hidden=all", "hidden=only", "favorite=true", "tag=Family", "place=Rome", "from=2012-01-01", "to=2000-01-01"} {
			r := httptest.NewRequest("GET", "/api/people?"+filters, nil)
			visible, args, err := l.curationSelection(r, "1=1", nil)
			if err != nil {
				t.Fatal(err)
			}
			rows, err := l.ai.read.Query(`SELECT p.id,p.name,count(DISTINCT f.image_id)`+personYearRangeSQL+`
 FROM people p JOIN faces f ON f.person_id=p.id JOIN ai_images a ON a.image_id=f.image_id
 JOIN catalog.images i INDEXED BY image_person_capture ON i.id=a.image_id AND i.signature=a.signature
 WHERE a.models=? AND a.error='' AND p.merged_into IS NULL
 AND i.id IN(SELECT id FROM catalog.images WHERE id=f.image_id AND `+visible+`)
 GROUP BY p.id ORDER BY count(DISTINCT f.image_id) DESC,p.id`, append([]any{l.ai.models}, args...)...)
			if err != nil {
				t.Fatal(err)
			}
			want := []personSummary{}
			for rows.Next() {
				var p personSummary
				if err := rows.Scan(&p.ID, &p.Name, &p.Count, &p.FirstYear, &p.LastYear); err != nil {
					t.Fatal(err)
				}
				p.ImageURL = fmt.Sprintf("/api/people/%d/image", p.ID)
				if hidden := r.URL.Query().Get("hidden"); hidden == "all" || hidden == "only" {
					p.ImageURL += "?hidden=" + hidden
				}
				want = append(want, p)
			}
			err = rows.Err()
			rows.Close()
			if err != nil {
				t.Fatal(err)
			}
			for offset := 0; offset <= len(want)+1; offset++ {
				var result struct {
					Items         []personSummary
					Total, Offset int
				}
				path := fmt.Sprintf("/api/people?%s&limit=1&offset=%d", filters, offset)
				if getJSON(t, s, path, &result) != 200 || result.Total != len(want) || result.Offset != offset || !reflect.DeepEqual(result.Items, want[min(offset, len(want)):min(offset+1, len(want))]) {
					t.Fatalf("%s: got=%+v want=%+v", path, result, want)
				}
			}
		}
	}
	compare()
	if _, err := l.db.Exec("UPDATE images SET signature='changed' WHERE id=?", imageID("a.jpg")); err != nil {
		t.Fatal(err)
	}
	compare()
	if _, err := l.ai.db.Exec("UPDATE ai_images SET error='failed' WHERE image_id=?", imageID("album_100%/c.jpg")); err != nil {
		t.Fatal(err)
	}
	compare()
}
