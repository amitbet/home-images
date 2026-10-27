package main

import (
	"context"
	"net/http/httptest"
	"testing"
	"time"
)

// A busy scanner/maintenance connection must not block cached image delivery.
func TestImageServingDoesNotWaitForCatalogWriter(t *testing.T) {
	l, _, _ := fixture(t)
	job := testJob(t, l, "a.jpg")
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	conn, err := l.db.Conn(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	p, err := l.get(ctx, job.ID)
	if err != nil {
		t.Fatal("image metadata queued behind maintenance", err)
	}
	if p.ID != job.ID {
		t.Fatal(p.ID)
	}
	rec := httptest.NewRecorder()
	req := httptest.NewRequest("GET", "/health", nil).WithContext(ctx)
	if err := l.health(rec, req); err != nil {
		t.Fatal("health queued behind maintenance", err)
	}
	if rec.Code != 200 {
		t.Fatal(rec.Code)
	}
	rec = httptest.NewRecorder()
	req = httptest.NewRequest("GET", "/api/images/"+job.ID+"/thumbnail", nil).WithContext(ctx)
	req.SetPathValue("id", job.ID)
	if err := l.thumbnail(rec, req); err != nil {
		t.Fatal("thumbnail queued behind maintenance", err)
	}
	if rec.Code != 200 || rec.Body.Len() == 0 {
		t.Fatal("thumbnail missing", rec.Code)
	}
}

func TestAbandonedSuggestionDoesNotQueue(t *testing.T) {
	l, _, _ := fixture(t)
	l.ai.suggestionSlot <- struct{}{}
	defer func() { <-l.ai.suggestionSlot }()
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	req := httptest.NewRequest("GET", "/api/people/1/suggestions", nil).WithContext(ctx)
	req.SetPathValue("person", "1")
	if err := l.ai.personSuggestions(httptest.NewRecorder(), req); err != context.Canceled {
		t.Fatal(err)
	}
}
