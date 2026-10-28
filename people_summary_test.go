package main

import (
	"context"
	"errors"
	"testing"
	"time"
)

func TestPeopleSummaryReuseAndInvalidation(t *testing.T) {
	l, s, _ := fixture(t)
	if err := l.ai.save(context.Background(), testJob(t, l, "a.jpg"), syntheticPrediction()); err != nil {
		t.Fatal(err)
	}
	var result struct {
		Items []personSummary
		Total int
	}
	read := func(query string) {
		t.Helper()
		if getJSON(t, s, "/api/people?summary=true&"+query, &result) != 200 {
			t.Fatal(result)
		}
	}
	read("")
	if result.Total != 1 || len(result.Items) != 1 || result.Items[0].Count != 1 {
		t.Fatal(result)
	}
	id := result.Items[0].ID
	first := l.ai.peopleSummary
	read("limit=1&offset=1")
	if result.Total != 1 || len(result.Items) != 0 || l.ai.peopleSummary != first {
		t.Fatal("pagination rebuilt summary", result)
	}
	postTest(t, s, "/api/people/"+fmtID(id), `{"name":"Dana"}`)
	read("")
	if l.ai.peopleSummary == first || result.Items[0].Name != "Dana" {
		t.Fatal("rename did not invalidate", result)
	}
	postTest(t, s, "/api/curation/"+imageID("a.jpg"), `{"hidden":true}`)
	read("")
	if result.Total != 0 || len(result.Items) != 0 {
		t.Fatal("hidden group leaked", result)
	}
	read("hidden=only")
	if result.Total != 1 || len(result.Items) != 1 || result.Items[0].Name != "Dana" {
		t.Fatal("hidden filter used default snapshot", result)
	}
	// Newly indexed photos appear on the next refresh without a manual edit.
	if err := l.ai.save(context.Background(), testJob(t, l, "b.jpg"), syntheticPrediction()); err != nil {
		t.Fatal(err)
	}
	read("refresh=1")
	if result.Total != 1 || result.Items[0].Count != 1 {
		t.Fatal("refresh missed indexing", result)
	}
	first = l.ai.peopleSummary
	l.ai.peopleMu.Lock()
	first.created = time.Now().Add(-3 * peopleSummaryInterval)
	l.ai.peopleMu.Unlock()
	read("")
	if l.ai.peopleSummary == first {
		t.Fatal("expired snapshot never refreshed")
	}
}

func TestPeopleSummaryWaitCanBeCancelled(t *testing.T) {
	l, _, _ := fixture(t)
	l.ai.peopleBuilding = make(chan struct{})
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := l.ai.peopleSummaries(ctx, false); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
}
