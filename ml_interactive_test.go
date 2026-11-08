package main

import (
	"context"
	"errors"
	"net/http"
	"testing"
	"time"
)

type mlRoundTripFunc func(*http.Request) (*http.Response, error)

func (f mlRoundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func TestTextVectorBoundsInteractiveInferenceAndPreservesCache(t *testing.T) {
	for _, parentTimeout := range []time.Duration{time.Minute, time.Second} {
		t.Run(parentTimeout.String(), func(t *testing.T) {
			calls := 0
			client := newML(MLConfig{URL: "http://worker", ClipModel: "test"})
			client.http.Transport = mlRoundTripFunc(func(r *http.Request) (*http.Response, error) {
				calls++
				deadline, ok := r.Context().Deadline()
				maximum := min(parentTimeout, 15*time.Second)
				if !ok || time.Until(deadline) > maximum || time.Until(deadline) < maximum-time.Second/2 {
					t.Errorf("interactive deadline = %v, want at most %v from now", deadline, maximum)
				}
				return nil, errors.New("offline")
			})
			a := &AI{ml: client, textCache: map[string]vector{"cached": {1, 0}}}
			ctx, cancel := context.WithTimeout(context.Background(), parentTimeout)
			defer cancel()
			if _, err := a.textVector(ctx, "tree"); err == nil {
				t.Fatal("unavailable worker reported success")
			}
			if _, ok := a.textCache["tree"]; ok {
				t.Fatal("failed inference cached")
			}
			if _, err := a.textVector(ctx, "cached"); err != nil {
				t.Fatal(err)
			}
			if calls != 1 {
				t.Fatalf("cached query called inference: %d requests", calls)
			}
		})
	}
}
