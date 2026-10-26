package main

import (
	"context"
	"errors"
	"testing"
	"time"
)

func TestPrepareAIJobsOverlapBoundAndCancel(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	started := make(chan string, 3)
	jobs := []aiJob{{ID: "first"}, {ID: "second"}, {ID: "third"}}
	out := prepareAIJobs(ctx, jobs, func(ctx context.Context, j aiJob) ([]byte, error) {
		started <- j.ID
		return []byte(j.ID), nil
	})
	if first := <-out; first.job.ID != "first" {
		t.Fatal(first)
	}
	// While inference holds the first preview, the second must already prepare.
	for _, want := range []string{"first", "second"} {
		select {
		case got := <-started:
			if got != want {
				t.Fatalf("got %s, want %s", got, want)
			}
		case <-time.After(time.Second):
			t.Fatal("preparation did not overlap")
		}
	}
	// No third preview can be decoded until the consumer accepts the second.
	select {
	case got := <-started:
		t.Fatalf("unbounded prefetch: %s", got)
	case <-time.After(50 * time.Millisecond):
	}
	cancel()
	done := make(chan struct{})
	go func() {
		for range out {
		}
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("producer did not stop")
	}
}

func TestPrepareAIJobsErrorsAndOrder(t *testing.T) {
	failure := errors.New("decode failure")
	out := prepareAIJobs(context.Background(), []aiJob{{ID: "bad"}, {ID: "good"}}, func(ctx context.Context, j aiJob) ([]byte, error) {
		if j.ID == "bad" {
			return nil, failure
		}
		return []byte("preview"), nil
	})
	first := <-out
	second := <-out
	if first.job.ID != "bad" || !errors.Is(first.err, failure) || second.job.ID != "good" || string(second.preview) != "preview" {
		t.Fatal(first, second)
	}
	if _, ok := <-out; ok {
		t.Fatal("producer did not close")
	}
}

func TestMLIndexIntervalDefaults(t *testing.T) {
	for _, interval := range []int{0, 100, 2000} {
		c := MLConfig{IntervalMS: interval}
		c.defaults()
		if c.IntervalMS != interval {
			t.Fatalf("interval %d changed to %d", interval, c.IntervalMS)
		}
	}
}
