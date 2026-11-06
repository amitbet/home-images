package main

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestVideoWarmingCompletesAndWatcherQueuesChanges(t *testing.T) {
	l, _, root := fixture(t)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	// An image-only catalog must complete without decoding any photos.
	if err := l.warmThumbnails(ctx); err != nil {
		t.Fatal(err)
	}
	if jobs := l.takeThumbnailJobs(); len(jobs) != 0 {
		t.Fatal("photos were queued", jobs)
	}
	path := filepath.Join(root, "new.mp4")
	if err := os.WriteFile(path, []byte("video bytes"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := l.reconcileChangedPath(ctx, "new.mp4"); err != nil {
		t.Fatal(err)
	}
	jobs := l.takeThumbnailJobs()
	if len(jobs) != 1 || jobs[0].Path != "new.mp4" {
		t.Fatal("new video was not queued", jobs)
	}
	// Unchanged notifications must not wake another pass.
	if err := l.reconcileChangedPath(ctx, "new.mp4"); err != nil {
		t.Fatal(err)
	}
	if jobs := l.takeThumbnailJobs(); len(jobs) != 0 {
		t.Fatal("unchanged video requeued", jobs)
	}
	if err := os.WriteFile(path, []byte("changed video bytes"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := l.reconcileChangedPath(ctx, "new.mp4"); err != nil {
		t.Fatal(err)
	}
	jobs = l.takeThumbnailJobs()
	if len(jobs) != 1 {
		t.Fatal("changed video not queued", jobs)
	}
	key, err := l.previewKey(jobs[0], fmt.Sprintf("image:%d:%d:%d", thumbnailWidth, thumbnailHeight, thumbnailQuality))
	if err != nil {
		t.Fatal(err)
	}
	data := testJPEG(t, 16, 16)
	if err := l.thumbnails.store(key, data); err != nil {
		t.Fatal(err)
	}
	// A complete cache needs no decoder and the entire pass returns promptly.
	if err := l.warmThumbnails(ctx); err != nil {
		t.Fatal(err)
	}
	// Cached startup checks use catalog signatures, without reopening the HDD.
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	if cachedKey, err := l.warmingPreviewKey(ctx, jobs[0], fmt.Sprintf("image:%d:%d:%d", thumbnailWidth, thumbnailHeight, thumbnailQuality)); err != nil || cachedKey != key {
		t.Fatalf("cached check reopened original or changed key: %q %v", cachedKey, err)
	}
	if err := l.warmThumbnails(ctx); err != nil {
		t.Fatal(err)
	}
	after, ok := l.thumbnails.read(key)
	if !ok || string(after) != string(data) {
		t.Fatal("cached thumbnail was replaced")
	}
}

func TestThumbnailQueueCoalescesAndWakesAfterDrain(t *testing.T) {
	l := &Library{}
	p := Photo{ID: "one", Path: "one.mp4"}
	l.queueVideoThumbnail(p)
	p.ID = "updated"
	l.queueVideoThumbnail(p)
	jobs := l.takeThumbnailJobs()
	if len(jobs) != 1 || jobs[0].ID != "updated" {
		t.Fatal(jobs)
	}
	<-l.thumbnailWake
	l.queueVideoThumbnail(Photo{Path: "skip.jpg"})
	select {
	case <-l.thumbnailWake:
		t.Fatal("photo woke video worker")
	default:
	}
	l.queueVideoThumbnail(p)
	select {
	case <-l.thumbnailWake:
	case <-time.After(time.Second):
		t.Fatal("worker was not woken")
	}
	if len(l.takeThumbnailJobs()) != 1 {
		t.Fatal("new notification was lost")
	}
}

func TestThumbnailWorkerHandlesVideoAfterStartup(t *testing.T) {
	l, _, root := fixture(t)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { defer close(done); l.runThumbnailWarming(ctx) }()
	defer func() {
		cancel()
		select {
		case <-done:
		case <-time.After(5 * time.Second):
			t.Error("worker did not stop")
		}
	}()
	// Let the empty startup pass finish, so only a notification can wake it.
	time.Sleep(5500 * time.Millisecond)
	if err := os.WriteFile(filepath.Join(root, "later.mp4"), []byte("invalid video"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := l.reconcileChangedPath(ctx, "later.mp4"); err != nil {
		t.Fatal(err)
	}
	var id string
	if err := l.db.QueryRow("SELECT id FROM images WHERE path='later.mp4'").Scan(&id); err != nil {
		t.Fatal(err)
	}
	p, err := l.get(ctx, id)
	if err != nil {
		t.Fatal(err)
	}
	key, err := l.previewKey(p, fmt.Sprintf("image:%d:%d:%d", thumbnailWidth, thumbnailHeight, thumbnailQuality))
	if err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(10 * time.Second)
	for !l.thumbnails.exists(key) {
		if time.Now().After(deadline) {
			t.Fatal("notified video was not warmed after startup")
		}
		time.Sleep(20 * time.Millisecond)
	}
}
