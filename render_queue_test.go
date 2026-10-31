package main

import (
	"context"
	"strings"
	"testing"
	"time"
)

func renderContext(p renderPriority) context.Context {
	return context.WithValue(context.Background(), renderPriorityKey{}, p)
}

func waitRenderQueue(t *testing.T, q *renderQueue, count int) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		q.mu.Lock()
		n := len(q.waiting)
		q.mu.Unlock()
		if n == count {
			return
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatal("render queue did not reach expected length", count)
}

func queuedRender(t *testing.T, q *renderQueue, ctx context.Context) <-chan func() {
	t.Helper()
	done := make(chan func(), 1)
	go func() {
		release, err := q.acquire(ctx)
		if err != nil {
			done <- nil
			return
		}
		done <- release
	}()
	return done
}

func takeRender(t *testing.T, done <-chan func()) func() {
	t.Helper()
	select {
	case release := <-done:
		if release == nil {
			t.Fatal("unexpected render cancellation")
		}
		return release
	case <-time.After(2 * time.Second):
		t.Fatal("render was not scheduled")
		return nil
	}
}

func TestRenderQueuePriorityCancellationAndAging(t *testing.T) {
	q := newRenderQueue(1)
	release, _ := q.acquire(context.Background())
	low := queuedRender(t, q, renderContext(renderPrefetch))
	waitRenderQueue(t, q, 1)
	normal := queuedRender(t, q, renderContext(renderNormal))
	waitRenderQueue(t, q, 2)
	ctx, cancel := context.WithCancel(renderContext(renderViewer))
	canceled := queuedRender(t, q, ctx)
	waitRenderQueue(t, q, 3)
	cancel()
	select {
	case r := <-canceled:
		if r != nil {
			t.Fatal("canceled waiter acquired a slot")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("canceled waiter remained queued")
	}
	viewer := queuedRender(t, q, renderContext(renderViewer))
	waitRenderQueue(t, q, 3)
	q.mu.Lock()
	q.waiting[0].since = time.Now().Add(-3 * time.Second)
	q.mu.Unlock()
	release()
	release = takeRender(t, viewer)
	if q.active() != 1 {
		t.Fatal("render limit changed")
	}
	// Old lower-priority work still makes progress under continuous viewers.
	q.mu.Lock()
	q.waiting[0].since = time.Now().Add(-3 * time.Second)
	q.mu.Unlock()
	for range 7 {
		more := queuedRender(t, q, renderContext(renderViewer))
		waitRenderQueue(t, q, 3)
		release()
		release = takeRender(t, more)
	}
	more := queuedRender(t, q, renderContext(renderViewer))
	waitRenderQueue(t, q, 3)
	release()
	release = takeRender(t, low)
	select {
	case <-normal:
		t.Fatal("grid overtook the aged request")
	default:
	}
	release()
	takeRender(t, more)()
	takeRender(t, normal)()
	if q.active() != 0 {
		t.Fatal("render slot leaked")
	}
}

func TestVisibleRequestPromotesSharedPrefetch(t *testing.T) {
	q := newRenderQueue(1)
	release, _ := q.acquire(context.Background())
	cache := newThumbnailCache(t.TempDir())
	key := strings.Repeat("c", 64)
	built := make(chan struct{}, 1)
	errors := make(chan error, 2)
	build := func(ctx context.Context) ([]byte, error) {
		r, err := q.acquire(ctx)
		if err != nil {
			return nil, err
		}
		defer r()
		built <- struct{}{}
		return []byte{0xff, 0xd8, 1, 0xff, 0xd9}, nil
	}
	go func() { _, err := cache.get(renderContext(renderPrefetch), key, build); errors <- err }()
	waitRenderQueue(t, q, 1)
	grid := queuedRender(t, q, renderContext(renderNormal))
	waitRenderQueue(t, q, 2)
	go func() { _, err := cache.get(renderContext(renderViewer), key, build); errors <- err }()
	deadline := time.Now().Add(2 * time.Second)
	for {
		cache.mu.Lock()
		promoted := cache.flights[key].demand.priority.Load() == int32(renderViewer)
		cache.mu.Unlock()
		if promoted {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("shared preload was not promoted")
		}
		time.Sleep(time.Millisecond)
	}
	release()
	for range 2 {
		select {
		case err := <-errors:
			if err != nil {
				t.Fatal(err)
			}
		case <-time.After(2 * time.Second):
			t.Fatal("shared render blocked behind grid")
		}
	}
	if len(built) != 1 {
		t.Fatal("shared preview decoded more than once")
	}
	takeRender(t, grid)()
}

func TestCanceledGrantedRenderDoesNotLeak(t *testing.T) {
	for range 100 {
		q := newRenderQueue(1)
		release, _ := q.acquire(context.Background())
		ctx, cancel := context.WithCancel(context.Background())
		done := queuedRender(t, q, ctx)
		waitRenderQueue(t, q, 1)
		cancel()
		release()
		select {
		case r := <-done:
			if r != nil {
				r()
			}
		case <-time.After(2 * time.Second):
			t.Fatal("cancel/grant race stalled")
		}
		if q.active() != 0 {
			t.Fatal("cancel/grant race leaked slot")
		}
	}
}
