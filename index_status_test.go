package main

import (
	"context"
	"testing"
	"time"
)

func TestIndexStatusCountsReuseAndRefresh(t *testing.T) {
	l, s, _ := fixture(t)
	ctx := context.Background()
	a := l.ai
	first := testJob(t, l, "a.jpg")
	if err := a.save(ctx, first, syntheticPrediction()); err != nil {
		t.Fatal(err)
	}
	counts, err := a.countIndex(ctx)
	if err != nil || counts != (indexCounts{total: 5, indexed: 1, faces: 1, people: 1}) {
		t.Fatal(counts, err)
	}
	// A polling tab must reuse the snapshot even while maintenance occupies
	// every status-read connection, while still returning live pause state.
	a.read.SetMaxOpenConns(1)
	conn, err := a.read.Conn(ctx)
	if err != nil {
		t.Fatal(err)
	}
	a.paused.Store(true)
	var status struct{ Paused bool }
	if getJSON(t, s, "/api/indexing", &status) != 200 || !status.Paused {
		t.Error("cached counts hid live pause state")
	}
	conn.Close()
	// Expiry must pick up changed signatures and exclude their stale faces.
	if _, err := l.db.Exec("UPDATE images SET signature='changed' WHERE id=?", first.ID); err != nil {
		t.Fatal(err)
	}
	a.statusMu.Lock()
	a.statusCountedAt = time.Now().Add(-11 * time.Second)
	a.statusMu.Unlock()
	counts, err = a.countIndex(ctx)
	if err != nil || counts != (indexCounts{total: 5}) {
		t.Fatal(counts, err)
	}
}

func TestPausedPeopleWarmingDoesNotWaitForDatabase(t *testing.T) {
	l, _, _ := fixture(t)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Millisecond)
	defer cancel()
	l.ai.read.SetMaxOpenConns(1)
	conn, err := l.ai.read.Conn(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	l.ai.paused.Store(true)
	if err := l.warmPeoplePass(ctx); err != context.DeadlineExceeded {
		t.Fatal(err)
	}
	if l.ai.read.Stats().WaitCount != 0 {
		t.Fatal("paused warmer attempted a database query")
	}
}

func TestIndexCountsRefreshWhenFilesMoveToTrash(t *testing.T) {
	l, s, _ := bulkFixture(t)
	ctx := context.Background()
	before, err := l.ai.countIndex(ctx)
	if err != nil {
		t.Fatal(err)
	}
	id := previewBatch(t, s)
	startBatchTest(t, l, s, id, "quarantine")
	if done, failed := waitBatch(t, l, id); done != 2 || failed != 0 {
		t.Fatal(done, failed)
	}
	after, err := l.ai.countIndex(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if after.total != before.total-2 {
		t.Fatalf("trash did not update total: before %d after %d", before.total, after.total)
	}
	restore := startBatchTest(t, l, s, id, "restore")
	if done, failed := waitBatch(t, l, restore); done != 2 || failed != 0 {
		t.Fatal(done, failed)
	}
	restored, err := l.ai.countIndex(ctx)
	if err != nil || restored.total != before.total {
		t.Fatalf("restore total %d expected %d: %v", restored.total, before.total, err)
	}
}
