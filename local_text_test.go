package main

import (
	"context"
	"encoding/json"
	"net/http"
	"os"
	"testing"
)

func TestLocalTextSearchWithoutRemoteWorker(t *testing.T) {
	directory := os.Getenv("HOMEIMAGES_TEST_TEXT_ASSETS")
	if directory == "" {
		t.Skip("local text assets not configured")
	}
	l, s, _ := fixture(t)
	l.config.ML.TextModelDirectory = directory
	l.config.ML.TextEncoder = "local"
	l.config.ML.URL = ""
	// An uncached query must never contact the remote worker.
	l.ai.ml.http.Transport = mlRoundTripFunc(func(*http.Request) (*http.Response, error) {
		t.Error("local text search called remote worker")
		return nil, context.Canceled
	})
	query, err := l.ai.textVector(context.Background(), "Amit near a tree")
	if err != nil {
		t.Fatal(err)
	}
	if len(query) != 512 {
		t.Fatal(len(query))
	}
	pred := syntheticPrediction()
	pred.Clip, err = json.Marshal(query)
	if err != nil {
		t.Fatal(err)
	}
	job := testJob(t, l, "a.jpg")
	if err = l.ai.save(context.Background(), job, pred); err != nil {
		t.Fatal(err)
	}
	var person int64
	if err = l.ai.db.QueryRow("SELECT person_id FROM faces WHERE image_id=?", job.ID).Scan(&person); err != nil {
		t.Fatal(err)
	}
	var result struct {
		Items    []Photo
		Semantic bool
	}
	// Use a second uncached query through the full API, combining a face filter.
	if status := getJSON(t, s, "/api/search?mode=semantic&q=tree&person="+fmtID(person), &result); status != 200 || !result.Semantic || len(result.Items) != 1 || result.Items[0].ID != job.ID {
		t.Fatalf("offline semantic search: status=%d result=%+v", status, result)
	}
}

func TestLocalEncoderDoesNotFallbackOnMissingAssets(t *testing.T) {
	l, _, _ := fixture(t)
	l.config.ML.TextEncoder = "local"
	l.config.ML.TextModelDirectory = t.TempDir()
	l.ai.ml.http.Transport = mlRoundTripFunc(func(*http.Request) (*http.Response, error) {
		t.Error("missing local assets fell back to remote")
		return nil, context.Canceled
	})
	if _, err := l.ai.textVector(context.Background(), "tree"); err == nil {
		t.Fatal("missing assets accepted")
	}
}
