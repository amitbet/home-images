package main

import (
	"context"
	"sync"
	"sync/atomic"
	"time"
)

type renderPriority int32

const (
	renderViewer renderPriority = iota
	renderNormal
	renderPrefetch
)

type renderPriorityKey struct{}
type renderDemandKey struct{}

// A shared cache flight can be promoted when a speculative image becomes visible.
type renderDemand struct{ priority atomic.Int32 }

func newRenderDemand(ctx context.Context) *renderDemand {
	d := &renderDemand{}
	d.priority.Store(int32(priorityFromContext(ctx)))
	return d
}

func priorityFromContext(ctx context.Context) renderPriority {
	if p, ok := ctx.Value(renderPriorityKey{}).(renderPriority); ok {
		return p
	}
	return renderNormal
}

func (d *renderDemand) promote(p renderPriority) {
	for old := d.priority.Load(); int32(p) < old; old = d.priority.Load() {
		if d.priority.CompareAndSwap(old, int32(p)) {
			return
		}
	}
}

type renderWaiter struct {
	ready  chan struct{}
	demand *renderDemand
	since  time.Time
}

type renderQueue struct {
	mu            sync.Mutex
	limit         int
	running       int
	priorityBurst int
	waiting       []*renderWaiter
}

func newRenderQueue(limit int) *renderQueue { return &renderQueue{limit: limit} }

func (q *renderQueue) active() int {
	if q == nil {
		return 0
	}
	q.mu.Lock()
	defer q.mu.Unlock()
	return q.running
}

func (q *renderQueue) acquire(ctx context.Context) (func(), error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if q == nil {
		return func() {}, nil
	}
	demand, _ := ctx.Value(renderDemandKey{}).(*renderDemand)
	if demand == nil {
		demand = newRenderDemand(ctx)
	}
	w := &renderWaiter{ready: make(chan struct{}), demand: demand, since: time.Now()}
	q.mu.Lock()
	if q.running < q.limit {
		q.running++
		close(w.ready)
	} else {
		q.waiting = append(q.waiting, w)
	}
	q.mu.Unlock()
	select {
	case <-w.ready:
		if err := ctx.Err(); err != nil {
			q.release()
			return nil, err
		}
		return q.release, nil
	case <-ctx.Done():
		q.mu.Lock()
		for i, pending := range q.waiting {
			if pending == w {
				q.waiting = append(q.waiting[:i], q.waiting[i+1:]...)
				q.mu.Unlock()
				return nil, ctx.Err()
			}
		}
		// A release granted this slot concurrently with cancellation.
		q.releaseLocked()
		q.mu.Unlock()
		return nil, ctx.Err()
	}
}

func (q *renderQueue) release() {
	q.mu.Lock()
	defer q.mu.Unlock()
	q.releaseLocked()
}

func (q *renderQueue) releaseLocked() {
	if len(q.waiting) == 0 {
		q.priorityBurst = 0
		q.running--
		return
	}
	best := 0
	for i, w := range q.waiting {
		if w.demand.priority.Load() < q.waiting[best].demand.priority.Load() {
			best = i
		}
	}
	// Let visible work overtake old grid/preload requests too. After eight
	// overtakes, grant the oldest aged request so lower classes cannot starve.
	if q.priorityBurst >= 8 && time.Since(q.waiting[0].since) >= 2*time.Second {
		best = 0
	}
	if best == 0 {
		q.priorityBurst = 0
	} else {
		q.priorityBurst++
	}
	w := q.waiting[best]
	q.waiting = append(q.waiting[:best], q.waiting[best+1:]...)
	close(w.ready)
}
