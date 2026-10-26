package main

import (
	"context"
	"sync"
	"time"
)

type preparedAIJob struct {
	job     aiJob
	preview []byte
	err     error
}
type aiPipelineResult struct {
	preview        []byte
	job            aiJob
	prediction     mlPrediction
	err            error
	inferenceError bool
}

// One producer, a bounded preview queue, concurrent remote inference, and one
// consumer. A remote outage cancels and drains the batch; uncommitted jobs remain
// eligible in SQL and are retried without advancing the caller's cursor.
func runAIPipeline(ctx context.Context, jobs []aiJob, workers int,
	prepare func(context.Context, aiJob) ([]byte, error),
	predict func(context.Context, []byte) (mlPrediction, error),
	consume func(aiPipelineResult) error) error {
	workers = max(1, min(4, workers))
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	prepared := make(chan preparedAIJob, 2)
	results := make(chan aiPipelineResult, 2)
	var producer, consumers sync.WaitGroup
	producer.Add(1)
	go func() {
		defer producer.Done()
		defer close(prepared)
		for _, job := range jobs {
			if ctx.Err() != nil {
				return
			}
			preview, err := prepare(ctx, job)
			select {
			case prepared <- preparedAIJob{job, preview, err}:
			case <-ctx.Done():
				return
			}
		}
	}()
	for range workers {
		consumers.Add(1)
		go func() {
			defer consumers.Done()
			for work := range prepared {
				if ctx.Err() != nil {
					return
				}
				result := aiPipelineResult{job: work.job, err: work.err, preview: work.preview}
				if work.err == nil {
					result.prediction, result.err = predict(ctx, work.preview)
					result.inferenceError = result.err != nil
				}
				work.preview = nil
				select {
				case results <- result:
				case <-ctx.Done():
					return
				}
				// Wait for the consumer to cancel the batch after an outage;
				// do not start another request while the error is queued.
				if result.inferenceError {
					return
				}
			}
		}()
	}
	go func() { consumers.Wait(); close(results) }()
	var first error
	for result := range results {
		if first == nil && ctx.Err() == nil {
			if err := consume(result); err != nil {
				first = err
				cancel()
			}
		}
	}
	producer.Wait()
	if first != nil {
		return first
	}
	return ctx.Err()
}
func (a *AI) pipelineTimings() map[string]float64 {
	mean := func(total, count int64) float64 {
		if count == 0 {
			return 0
		}
		return float64(total) / float64(count) / 1000
	}
	return map[string]float64{"preview": mean(a.pipeline.previewUS.Load(), a.pipeline.prepared.Load()), "inference": mean(a.pipeline.inferenceUS.Load(), a.pipeline.inferred.Load()), "save": mean(a.pipeline.saveUS.Load(), a.pipeline.completed.Load()), "foreground_or_pause_wait_total": float64(a.pipeline.waitUS.Load()) / 1000}
}

func (a *AI) pipelineRate() float64 {
	start := a.pipeline.started.Load()
	if start == 0 {
		return 0
	}
	minutes := time.Since(time.Unix(0, start)).Minutes()
	if minutes <= 0 {
		return 0
	}
	return float64(a.pipeline.completed.Load()) / minutes
}
