package main

import (
	"context"
	"encoding/json"
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
