package main

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"
)

func TestAIPipelineBoundedConcurrentAndSerialCommit(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	jobs := make([]aiJob, 10)
	for i := range jobs {
		jobs[i].ID = string(rune('a' + i))
	}
	release := make(chan struct{})
	entered := make(chan struct{}, 2)
	done := make(chan error, 1)
	var prepared, active, peak atomic.Int64
	seen := map[string]bool{}
	go func() {
		done <- runAIPipeline(ctx, jobs, 2,
			func(context.Context, aiJob) ([]byte, error) { prepared.Add(1); return []byte("jpeg"), nil },
			func(ctx context.Context, _ []byte) (mlPrediction, error) {
				n := active.Add(1)
				defer active.Add(-1)
				for old := peak.Load(); n > old; old = peak.Load() {
					if peak.CompareAndSwap(old, n) {
						break
					}
				}
				select {
				case entered <- struct{}{}:
				default:
				}
				select {
				case <-release:
					return mlPrediction{}, nil
				case <-ctx.Done():
					return mlPrediction{}, ctx.Err()
				}
			},
			func(r aiPipelineResult) error {
				if seen[r.job.ID] {
					t.Error("duplicate commit", r.job.ID)
				}
				seen[r.job.ID] = true
				return nil
			})
	}()
	for range 2 {
		select {
		case <-entered:
		case <-ctx.Done():
			t.Fatal("inference did not overlap")
		}
	}
	time.Sleep(20 * time.Millisecond)
	if prepared.Load() > 5 {
		t.Error("preview queue unbounded", prepared.Load())
	}
	close(release)
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	if peak.Load() != 2 || len(seen) != len(jobs) {
		t.Fatal("pipeline lost work or exceeded concurrency", peak.Load(), len(seen))
	}
}
func TestAIPipelineOutageCancelsRemainingWork(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	outage := errors.New("ML unavailable")
	var calls atomic.Int64
	err := runAIPipeline(ctx, make([]aiJob, 100), 2,
		func(context.Context, aiJob) ([]byte, error) { return []byte("jpeg"), nil },
		func(ctx context.Context, _ []byte) (mlPrediction, error) {
			if calls.Add(1) == 1 {
				return mlPrediction{}, outage
			}
			<-ctx.Done()
			return mlPrediction{}, ctx.Err()
		},
		func(r aiPipelineResult) error {
			if r.inferenceError {
				return r.err
			}
			return nil
		})
	if !errors.Is(err, outage) || calls.Load() > 2 {
		t.Fatal("outage did not stop bounded pipeline", err, calls.Load())
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
