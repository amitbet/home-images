package main

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestDirectoryOverlapCoverageAndNesting(t *testing.T) {
	ctx := context.Background()
	f := func(id, path, hash, asset string) overlapFile { return overlapFile{id, path, hash, asset, 100} }
	files := []overlapFile{f("1", "ALBUM/A/a.jpg", "h1", "c1"), f("2", "ALBUM/A/sub/b.jpg", "h2", "c2"), f("3", "ALBUM/A/variant.jpg", "h3", "c3"), f("4", "ALBUM/A/new.jpg", "", ""), f("5", "ALBUM/B/x.jpg", "h1", "c1"), f("6", "ALBUM/B/sub/y.jpg", "h2", "c2"), f("7", "ALBUM/B/other.jpg", "h4", "c3"), f("8", "root.jpg", "h1", "c1")}
	pairs, dirs, err := analyzeDirectoryOverlaps(ctx, files, "", 2)
	if err != nil || dirs != 2 || len(pairs) != 2 {
		t.Fatal(pairs, dirs, err)
	}
	for _, p := range pairs {
		if p.Source == "ALBUM/A" {
			if p.SourceFiles != 4 || p.ExactFiles != 2 || p.ContentFiles != 3 || p.PendingFiles != 1 || p.Full || p.Bytes != 200 {
				t.Fatal(p)
			}
		}
	}
	nested, _, err := analyzeDirectoryOverlaps(ctx, files, "ALBUM", 1)
	if err != nil || len(nested) != 2 {
		t.Fatal(nested, err)
	}
	if disjointDirectories("ALBUM/A", "ALBUM/A/sub") || disjointDirectories("", "ALBUM") || !disjointDirectories("ALBUM/A", "ALBUM/AB") {
		t.Fatal("unsafe directory boundaries")
	}
}
func bulkFixture(t *testing.T) (*Library, *httptest.Server, string) {
	t.Helper()
	l, s, root := fixture(t)
	data, err := os.ReadFile("testdata/orientation6.jpg")
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"bulk/A/one.jpg", "bulk/A/sub/two.jpg", "bulk/B/copy.jpg", "bulk/B/sub/copy2.jpg"} {
		full := filepath.Join(root, filepath.FromSlash(name))
		if err = os.MkdirAll(filepath.Dir(full), 0755); err != nil {
			t.Fatal(err)
		}
		if err = os.WriteFile(full, data, 0644); err != nil {
			t.Fatal(err)
		}
	}
	// A different file checksum and the same image data is a preserved metadata variant.
	if err = os.WriteFile(filepath.Join(root, "bulk/A/variant.jpg"), append(append([]byte{}, data...), []byte("metadata variant")...), 0644); err != nil {
		t.Fatal(err)
	}
	unique, err := os.ReadFile("testdata/sample.bmp")
	if err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(filepath.Join(root, "bulk/A/unique.bmp"), unique, 0644); err != nil {
		t.Fatal(err)
	}
	l.Scan(context.Background())
	for i := 0; i < 3; i++ {
		hashFixture(t, l)
	}
	return l, s, root
}
func previewBatch(t *testing.T, s *httptest.Server) string {
	t.Helper()
	w := httptest.NewRecorder()
	r := httptest.NewRequest("POST", "/api/duplicates/plan", strings.NewReader(`{"source":"bulk/A","keep":"bulk/B"}`))
	r.Header.Set("X-Home-Images", "1")
	s.Config.Handler.ServeHTTP(w, r)
	if w.Code != 200 {
		t.Fatal(w.Code, w.Body.String())
	}
	var out struct {
		ID    string
		Count int
	}
	if err := json.Unmarshal(w.Body.Bytes(), &out); err != nil {
		t.Fatal(err)
	}
	if out.Count != 2 {
		t.Fatal("wrong planned count", w.Body.String())
	}
	return out.ID
}
func waitBatch(t *testing.T, l *Library, id string) (int, int) {
	t.Helper()
	deadline := time.Now().Add(20 * time.Second)
	for time.Now().Before(deadline) {
		l.duplicateJobMu.Lock()
		_, running := l.duplicateJobs[id]
		l.duplicateJobMu.Unlock()
		if !running {
			var done, failed int
			if err := l.db.QueryRow("SELECT coalesce(sum(state='done'),0),coalesce(sum(state='error'),0) FROM duplicate_batch_items WHERE batch_id=?", id).Scan(&done, &failed); err != nil {
				t.Fatal(err)
			}
			return done, failed
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatal("job did not finish")
	return 0, 0
}
func startBatchTest(t *testing.T, l *Library, s *httptest.Server, id, action string) string {
	t.Helper()
	w := httptest.NewRecorder()
	r := httptest.NewRequest("POST", "/api/duplicates/batch-start", strings.NewReader(fmt.Sprintf(`{"id":%q,"action":%q}`, id, action)))
	r.Header.Set("X-Home-Images", "1")
	s.Config.Handler.ServeHTTP(w, r)
	if w.Code != 200 {
		t.Fatal(w.Code, w.Body.String())
	}
	var out struct{ ID string }
	json.Unmarshal(w.Body.Bytes(), &out)
	return out.ID
}
func TestBulkDirectoryCleanupRestoreAndPurge(t *testing.T) {
	l, s, root := bulkFixture(t)
	plan, err := l.buildDirectoryPlan(context.Background(), "bulk/A", "bulk/B")
	if err != nil || len(plan.Items) != 2 || plan.Variants != 1 || plan.Unique != 1 {
		t.Fatal(plan, err)
	}
	id := previewBatch(t, s)
	id = startBatchTest(t, l, s, id, "quarantine")
	if done, failed := waitBatch(t, l, id); done != 2 || failed != 0 {
		t.Fatal(done, failed)
	}
	for _, name := range []string{"bulk/A/variant.jpg", "bulk/A/unique.bmp", "bulk/B/copy.jpg", "bulk/B/sub/copy2.jpg"} {
		if _, e := os.Stat(filepath.Join(root, filepath.FromSlash(name))); e != nil {
			t.Fatal("preserved file missing", name, e)
		}
	}
	for _, name := range []string{"bulk/A/one.jpg", "bulk/A/sub/two.jpg"} {
		if _, e := os.Stat(filepath.Join(root, filepath.FromSlash(name))); !os.IsNotExist(e) {
			t.Fatal("not moved", name, e)
		}
	}
	restore := startBatchTest(t, l, s, id, "restore")
	if done, failed := waitBatch(t, l, restore); done != 2 || failed != 0 {
		t.Fatal(done, failed)
	}
	second := previewBatch(t, s)
	second = startBatchTest(t, l, s, second, "quarantine")
	waitBatch(t, l, second)
	purge := startBatchTest(t, l, s, second, "purge")
	if done, failed := waitBatch(t, l, purge); done != 2 || failed != 0 {
		t.Fatal(done, failed)
	}
	var n int
	if err = l.db.QueryRow("SELECT count(*) FROM duplicate_quarantine").Scan(&n); err != nil || n != 0 {
		t.Fatal(n, err)
	}
}
func TestBulkCleanupRejectsStalePlanAndParents(t *testing.T) {
	l, s, root := bulkFixture(t)
	id := previewBatch(t, s)
	f, e := os.OpenFile(filepath.Join(root, "bulk/A/one.jpg"), os.O_APPEND|os.O_WRONLY, 0)
	if e != nil {
		t.Fatal(e)
	}
	f.Write([]byte("changed after review"))
	f.Close()
	id = startBatchTest(t, l, s, id, "quarantine")
	done, failed := waitBatch(t, l, id)
	if done != 1 || failed != 1 {
		t.Fatal("stale file not skipped", done, failed)
	}
	if _, e = os.Stat(filepath.Join(root, "bulk/A/one.jpg")); e != nil {
		t.Fatal(e)
	}
	if _, e = l.buildDirectoryPlan(context.Background(), "bulk", "bulk/B"); e == nil {
		t.Fatal("parent cleanup accepted")
	}
}
func TestBulkCleanupCancelAndResume(t *testing.T) {
	l, s, root := bulkFixture(t)
	id := previewBatch(t, s)
	l.scanMu.Lock()
	id = startBatchTest(t, l, s, id, "quarantine")
	if code := postTest(t, s, "/api/duplicates/batch-cancel", fmt.Sprintf(`{"id":%q}`, id)); code != 200 {
		l.scanMu.Unlock()
		t.Fatal(code)
	}
	l.scanMu.Unlock()
	done, failed := waitBatch(t, l, id)
	if done != 0 || failed != 0 {
		t.Fatal("cancelled job changed files", done, failed)
	}
	if _, err := os.Stat(filepath.Join(root, "bulk/A/one.jpg")); err != nil {
		t.Fatal(err)
	}
	id = startBatchTest(t, l, s, id, "resume")
	if done, failed = waitBatch(t, l, id); done != 2 || failed != 0 {
		t.Fatal(done, failed)
	}
}
func TestBulkCleanupRecoversRecordedMove(t *testing.T) {
	l, s, _ := bulkFixture(t)
	id := previewBatch(t, s)
	plan, err := l.buildDirectoryPlan(context.Background(), "bulk/A", "bulk/B")
	if err != nil {
		t.Fatal(err)
	}
	request := httptest.NewRequest("POST", "/", nil)
	result, err := l.quarantinePairExpected(request, plan.Items[0].KeepID, plan.Items[0].RemoveID, plan.Items[0].Hash)
	if err != nil {
		t.Fatal(err)
	}
	// Simulate a crash after the journal and move, before saving batch progress.
	id = startBatchTest(t, l, s, id, "quarantine")
	done, failed := waitBatch(t, l, id)
	if done != 2 || failed != 0 {
		t.Fatal(done, failed)
	}
	var token string
	if err = l.db.QueryRow("SELECT token FROM duplicate_batch_items WHERE batch_id=? AND sequence=0", id).Scan(&token); err != nil || token != result["token"] {
		t.Fatal("move recovery lost journal", token, err)
	}
}

func TestOverlapMatchesExactBytesAcrossFormatIdentities(t *testing.T) {
	files := []overlapFile{{"a", "A/photo.jpg", "exact-hash", "jpeg-domain", 100}, {"b", "B/photo.png", "exact-hash", "fallback-domain", 100}}
	pairs, _, err := analyzeDirectoryOverlaps(context.Background(), files, "", 1)
	if err != nil || len(pairs) != 2 {
		t.Fatal(pairs, err)
	}
	for _, p := range pairs {
		if !p.Full || p.ExactFiles != 1 || p.ContentFiles != 1 {
			t.Fatal(p)
		}
	}
}
func TestBulkCleanupPlanSurvivesServiceRestart(t *testing.T) {
	l, s, _ := bulkFixture(t)
	id := previewBatch(t, s)
	cfg := l.config
	if _, err := l.db.Exec("UPDATE duplicate_batches SET state='running' WHERE id=?", id); err != nil {
		t.Fatal(err)
	}
	l.Close()
	reopened, err := newLibrary(cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	server := httptest.NewServer(reopened.Handler())
	defer server.Close()
	var state string
	if err = reopened.db.QueryRow("SELECT state FROM duplicate_batches WHERE id=?", id).Scan(&state); err != nil || state != "interrupted" {
		t.Fatal("restart did not preserve interrupted plan", state, err)
	}
	id = startBatchTest(t, reopened, server, id, "resume")
	done, failed := waitBatch(t, reopened, id)
	if done != 2 || failed != 0 {
		t.Fatal(done, failed)
	}
}

func TestOverlapEndpointGroupsBothDirections(t *testing.T) {
	_, s, _ := bulkFixture(t)
	w := httptest.NewRecorder()
	s.Config.Handler.ServeHTTP(w, httptest.NewRequest("GET", "/api/duplicates/overlaps?scope=bulk&depth=1&minimum=1", nil))
	if w.Code != 200 {
		t.Fatal(w.Code, w.Body.String())
	}
	var result struct {
		Total int `json:"total"`
		Items []struct {
			First  directoryOverlap `json:"first"`
			Second directoryOverlap `json:"second"`
		} `json:"items"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &result); err != nil {
		t.Fatal(err)
	}
	if result.Total != 1 || len(result.Items) != 1 {
		t.Fatal(w.Body.String())
	}
	a, b := result.Items[0].First, result.Items[0].Second
	if a.Source != b.Keep || a.Keep != b.Source || a.SourceFiles == b.SourceFiles || a.ExactFiles != 2 || b.ExactFiles != 2 {
		t.Fatal(a, b)
	}
	w = httptest.NewRecorder()
	s.Config.Handler.ServeHTTP(w, httptest.NewRequest("GET", "/api/duplicates/overlaps?scope=bulk&depth=1&minimum=1&full=true", nil))
	if err := json.Unmarshal(w.Body.Bytes(), &result); err != nil || result.Total != 1 {
		t.Fatal(w.Body.String(), err)
	}
}

func TestConcurrentCleanupJobsCancelIndependently(t *testing.T) {
	l, s, _ := bulkFixture(t)
	first, second := previewBatch(t, s), previewBatch(t, s)
	if _, err := l.db.Exec("UPDATE duplicate_batch_items SET remove_id=remove_id||'-other', keep_id=keep_id||'-other' WHERE batch_id=?", second); err != nil {
		t.Fatal(err)
	}
	l.scanMu.Lock()
	startBatchTest(t, l, s, first, "quarantine")
	startBatchTest(t, l, s, second, "quarantine")
	l.duplicateJobMu.Lock()
	if len(l.duplicateJobs) != 2 {
		l.duplicateJobMu.Unlock()
		l.scanMu.Unlock()
		t.Fatal("both jobs should be active")
	}
	l.duplicateJobs[first]()
	l.duplicateJobMu.Unlock()
	l.scanMu.Unlock()
	waitBatch(t, l, first)
	done, failed := waitBatch(t, l, second)
	if done+failed != 2 {
		t.Fatalf("second job did not finish: %d done, %d failed", done, failed)
	}
}
