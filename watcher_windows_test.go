//go:build windows

package main

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestWatcherChangesAndRenamedDirectory(t *testing.T) {
	l, _, root := fixture(t)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { defer close(done); l.watchChanges(ctx) }()
	defer func() { cancel(); <-done }()
	// Give Windows time to arm the asynchronous notification request.
	time.Sleep(300 * time.Millisecond)
	source, err := os.ReadFile(filepath.Join(root, "a.jpg"))
	if err != nil {
		t.Fatal(err)
	}
	dir := filepath.Join(root, "watch-new")
	if err = os.Mkdir(dir, 0755); err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(filepath.Join(dir, "new.jpg"), source, 0644); err != nil {
		t.Fatal(err)
	}
	waitFor := func(path string, want int) {
		t.Helper()
		deadline := time.Now().Add(20 * time.Second)
		for time.Now().Before(deadline) {
			var n int
			if e := l.db.QueryRow("SELECT count(*) FROM images WHERE path=?", path).Scan(&n); e == nil && n == want {
				return
			}
			time.Sleep(100 * time.Millisecond)
		}
		t.Fatalf("watcher did not reach count %d for %s", want, path)
	}
	waitFor("watch-new/new.jpg", 1)
	if err = os.Rename(dir, filepath.Join(root, "watch-moved")); err != nil {
		t.Fatal(err)
	}
	waitFor("watch-new/new.jpg", 0)
	waitFor("watch-moved/new.jpg", 1)
	if err = os.Remove(filepath.Join(root, "watch-moved", "new.jpg")); err != nil {
		t.Fatal(err)
	}
	waitFor("watch-moved/new.jpg", 0)
}
