package main

import (
	"context"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"
)

func TestAutomaticScanWeeklyLimit(t *testing.T) {
	base := t.TempDir()
	root := filepath.Join(base, "photos")
	if err := os.Mkdir(root, 0755); err != nil {
		t.Fatal(err)
	}
	config := Config{Root: root, DataDir: filepath.Join(base, "data"), ScanInterval: 30, MaxPixels: 80000000}
	l, err := newLibrary(config)
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	l.scanIfDue(ctx)
	if l.Status().LastScan == nil {
		t.Fatal("initial scan did not run")
	}
	l.Close()
	l, err = newLibrary(config)
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()
	var wg sync.WaitGroup
	for range 8 {
		wg.Add(1)
		go func() { defer wg.Done(); l.scanIfDue(ctx) }()
	}
	wg.Wait()
	if l.Status().LastScan != nil {
		t.Fatal("restart or concurrent trigger bypassed weekly limit")
	}
	if l.scanInterval() != 7*24*time.Hour {
		t.Fatal("short interval not clamped")
	}
	if _, err = l.db.Exec("UPDATE settings SET value=? WHERE key='last_automatic_scan'", time.Now().Add(-7*24*time.Hour).Unix()); err != nil {
		t.Fatal(err)
	}
	l.scanIfDue(ctx)
	if l.Status().LastScan == nil {
		t.Fatal("scan did not run after seven days")
	}
	l.config.ScanInterval = 1209600
	if l.scanInterval() != 14*24*time.Hour {
		t.Fatal("longer interval not respected")
	}
}
