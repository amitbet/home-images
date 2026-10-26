package main

import (
	"context"
	"encoding/json"
	"fmt"
	"image/jpeg"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"
)

func syntheticPrediction() mlPrediction {
	return mlPrediction{Clip: json.RawMessage(`"[1,0,0]"`), Width: 64, Height: 96, Faces: []mlFace{{Box: faceBox{4, 8, 30, 40}, Score: .95, Embedding: json.RawMessage(`"[1,0,0]"`)}}}
}
func testJob(t *testing.T, l *Library, path string) aiJob {
	t.Helper()
	var j aiJob
	if err := l.db.QueryRow("SELECT id,signature,metadata FROM images WHERE path=?", path).Scan(&j.ID, &j.Signature, &j.Metadata); err != nil {
		t.Fatal(err)
	}
	return j
}
func postTest(t *testing.T, s *httptest.Server, path, body string) int {
	t.Helper()
	req, _ := http.NewRequest("POST", s.URL+path, strings.NewReader(body))
	req.Header.Set("X-Home-Images", "1")
	req.Header.Set("Content-Type", "application/json")
	resp, err := s.Client().Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		b, _ := io.ReadAll(resp.Body)
		t.Logf("POST %s: %s", path, b)
	}
	return resp.StatusCode
}
func TestAIIndexSearchNamesAndInvalidation(t *testing.T) {
	l, s, _ := fixture(t)
	ctx := context.Background()
	a := l.ai
	j := testJob(t, l, "a.jpg")
	if err := a.save(ctx, j, syntheticPrediction()); err != nil {
		t.Fatal(err)
	}
	j2 := testJob(t, l, "b.jpg")
	if err := a.save(ctx, j2, syntheticPrediction()); err != nil {
		t.Fatal(err)
	}
	var groups struct {
		Items []personSummary
		Total int
	}
	if getJSON(t, s, "/api/people", &groups) != 200 || groups.Total != 1 || groups.Items[0].Count != 2 {
		t.Fatalf("grouping: %+v", groups)
	}
	id := groups.Items[0].ID
	if status := postTest(t, s, "/api/people/"+fmtID(id), `{"name":"Dana <script>"}`); status != 200 {
		t.Fatal(status)
	}
	var named struct{ Items []personSummary }
	getJSON(t, s, "/api/people?q=Dana", &named)
	if len(named.Items) != 1 || named.Items[0].Name != "Dana <script>" {
		t.Fatal(named)
	}
	if postTest(t, s, "/api/indexing/control", `{"action":"pause"}`) != 200 || !a.paused.Load() {
		t.Fatal("pause failed")
	}
	var paused string
	if err := a.db.QueryRow("SELECT value FROM ai_settings WHERE key='paused'").Scan(&paused); err != nil || paused != "true" {
		t.Fatal("pause not persisted", err)
	}
	var result struct {
		Items []Photo
		Total int
	}
	reopened, err := newLibrary(l.config)
	if err != nil {
		t.Fatal(err)
	}
	if !reopened.ai.paused.Load() {
		t.Error("pause lost after reopening")
	}
	var restored string
	if err = reopened.ai.db.QueryRow("SELECT name FROM people WHERE id=?", id).Scan(&restored); err != nil || restored != "Dana <script>" {
		t.Error("name lost after reopening", err)
	}
	reopened.Close()
	if getJSON(t, s, "/api/search?person="+fmtID(id)+"&mode=metadata", &result) != 200 || result.Total != 2 {
		t.Fatal(result)
	}
	// Search must rank actual vectors and combine them with a person filter.
	a.lib.config.ML.URL = "http://unused"
	a.textCache["beach"] = vector{1, 0, 0}
	if getJSON(t, s, "/api/search?q=beach&person="+fmtID(id), &result) != 200 || len(result.Items) != 2 {
		t.Fatal(result)
	}
	// Updated source metadata invalidates both semantic and person matches before reindexing.
	if _, err := l.db.Exec("UPDATE images SET signature='changed' WHERE id=?", j.ID); err != nil {
		t.Fatal(err)
	}
	getJSON(t, s, "/api/search?person="+fmtID(id)+"&mode=metadata", &result)
	if result.Total != 1 {
		t.Fatalf("stale face was searchable: %+v", result)
	}
	pred := syntheticPrediction()
	pred.Clip = json.RawMessage(`"[0,0,0]"`)
	if err := a.save(ctx, j2, pred); err == nil {
		t.Fatal("invalid prediction committed")
	}
	getJSON(t, s, "/api/search?person="+fmtID(id)+"&mode=metadata", &result)
	if result.Total != 1 {
		t.Fatal("failed transaction destroyed prior result")
	}
	response, err := s.Client().Get(s.URL + "/api/people/" + fmtID(id) + "/image")
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	if response.StatusCode != 200 || response.Header.Get("Content-Type") != "image/jpeg" {
		t.Fatal("face thumbnail", response.Status)
	}
}
func fmtID(id int64) string { return json.Number(strconv.FormatInt(id, 10)).String() }

func TestDecimalFaceCoordinates(t *testing.T) {
	var p mlPrediction
	err := json.Unmarshal([]byte(`{"imageWidth":1280,"imageHeight":847,"clip":"[1,0,0]","facial-recognition":[{"boundingBox":{"x1":513.0,"y1":341.0,"x2":605.0,"y2":461.0},"score":0.95,"embedding":"[0,1,0]"}]}`), &p)
	if err != nil || len(p.Faces) != 1 || p.Faces[0].Box.X1 != 513 {
		t.Fatal("real ML decimal coordinates rejected", err, p)
	}
}

func TestFaceBoxesAtPreviewEdgesDoNotLosePhotoIndex(t *testing.T) {
	l, _, _ := fixture(t)
	p := syntheticPrediction()
	p.Faces[0].Box = faceBox{-5, -3, 70, 100}
	if err := l.ai.save(context.Background(), testJob(t, l, "a.jpg"), p); err != nil {
		t.Fatal(err)
	}
	var raw string
	if err := l.ai.db.QueryRow("SELECT box FROM faces WHERE image_id=?", imageID("a.jpg")).Scan(&raw); err != nil {
		t.Fatal(err)
	}
	var box faceBox
	if err := json.Unmarshal([]byte(raw), &box); err != nil {
		t.Fatal(err)
	}
	if box != (faceBox{0, 0, 64, 96}) {
		t.Fatal("box not clipped", box)
	}
	p.Faces[0].Box = faceBox{100, 200, 130, 250}
	if err := l.ai.save(context.Background(), testJob(t, l, "b.jpg"), p); err != nil {
		t.Fatal(err)
	}
	var count int
	if err := l.ai.db.QueryRow("SELECT count(*) FROM ai_images WHERE error='' AND clip IS NOT NULL").Scan(&count); err != nil || count != 2 {
		t.Fatal("unusable face lost valid content embedding", count, err)
	}
}
func TestMergeAndWriteProtection(t *testing.T) {
	l, s, _ := fixture(t)
	a := l.ai
	ctx := context.Background()
	first := testJob(t, l, "a.jpg")
	second := testJob(t, l, "b.jpg")
	if err := a.save(ctx, first, syntheticPrediction()); err != nil {
		t.Fatal(err)
	}
	other := syntheticPrediction()
	other.Faces[0].Embedding = json.RawMessage(`"[0,1,0]"`)
	if err := a.save(ctx, second, other); err != nil {
		t.Fatal(err)
	}
	var groups struct{ Items []personSummary }
	getJSON(t, s, "/api/people", &groups)
	if len(groups.Items) != 2 {
		t.Fatal(groups)
	}
	target, source := groups.Items[0].ID, groups.Items[1].ID
	if postTest(t, s, "/api/people/"+fmtID(source), `{"merge_into":`+fmtID(target)+`}`) != 200 {
		t.Fatal("merge failed")
	}
	getJSON(t, s, "/api/people", &groups)
	if len(groups.Items) != 1 || groups.Items[0].Count != 2 {
		t.Fatal(groups)
	}
	// New photos matching the merged representative must stay in the merged group.
	if err := a.save(ctx, testJob(t, l, "album_100%/c.jpg"), other); err != nil {
		t.Fatal(err)
	}
	getJSON(t, s, "/api/people", &groups)
	if len(groups.Items) != 1 || groups.Items[0].Count != 3 {
		t.Fatal("merge did not persist recognition aliases", groups)
	}
	req, _ := http.NewRequest("POST", s.URL+"/api/indexing/control", strings.NewReader(`{"action":"pause"}`))
	req.Header.Set("Origin", "http://hostile.example")
	req.Header.Set("X-Home-Images", "1")
	resp, err := s.Client().Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != 403 || a.paused.Load() {
		t.Fatal("cross-origin mutation allowed")
	}
	req, _ = http.NewRequest("POST", s.URL+"/api/indexing/control", strings.NewReader(`{"action":"pause"}`))
	resp, err = s.Client().Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != 403 {
		t.Fatal("missing custom header accepted")
	}
}
func TestSlowMLDoesNotBlockServing(t *testing.T) {
	l, s, _ := fixture(t)
	entered, release := make(chan struct{}), make(chan struct{})
	ml := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if err := r.ParseMultipartForm(1 << 20); err != nil {
			t.Error(err)
		}
		close(entered)
		<-release
		_ = json.NewEncoder(w).Encode(syntheticPrediction())
	}))
	defer ml.Close()
	l.ai.ml = newML(MLConfig{URL: ml.URL, ClipModel: "test", FaceModel: "test"})
	done := make(chan error, 1)
	go func() { _, err := l.ai.ml.predict(context.Background(), []byte("preview"), ""); done <- err }()
	<-entered
	func() {
		defer close(release)
		client := &http.Client{Timeout: time.Second}
		for _, path := range []string{"/health", "/api/next?mode=sequential", "/api/images/" + imageID("a.jpg") + "/image?width=64&height=64", "/api/indexing"} {
			resp, err := client.Get(s.URL + path)
			if err != nil {
				t.Error("foreground blocked by ML", path, err)
				continue
			}
			io.Copy(io.Discard, resp.Body)
			resp.Body.Close()
			if resp.StatusCode != 200 {
				t.Error(path, resp.Status)
			}
		}
	}()
	if err := <-done; err != nil {
		t.Fatal(err)
	}
}
func TestLiveMLProtocol(t *testing.T) {
	endpoint := os.Getenv("HOME_IMAGES_TEST_ML")
	if endpoint == "" {
		t.Skip("set HOME_IMAGES_TEST_ML for explicit live ML verification")
	}
	config := MLConfig{URL: endpoint}
	config.defaults()
	client := newML(config)
	defer client.http.CloseIdleConnections()
	data, err := os.ReadFile("testdata/orientation6.jpg")
	if err != nil {
		t.Fatal(err)
	}
	pred, err := client.predict(context.Background(), data, "")
	if err != nil {
		t.Fatal(err)
	}
	imageVector, err := parseVector(pred.Clip)
	if err != nil {
		t.Fatal(err)
	}
	text, err := client.predict(context.Background(), nil, "a family at the beach")
	if err != nil {
		t.Fatal(err)
	}
	textVector, err := parseVector(text.Clip)
	if err != nil {
		t.Fatal(err)
	}
	if len(imageVector) != len(textVector) {
		t.Fatal("incompatible text/image vectors")
	}
	t.Logf("Mac ML returned %d-dimensional image/text vectors and %d faces", len(imageVector), len(pred.Faces))
	// A real library preview exercises face outputs if a face is present.
	if source := os.Getenv("HOME_IMAGES_TEST_PHOTO_API"); source != "" {
		var p Photo
		resp, err := http.Get(source + "/api/next?mode=random")
		if err != nil {
			t.Fatal(err)
		}
		err = json.NewDecoder(resp.Body).Decode(&p)
		resp.Body.Close()
		if err != nil {
			t.Fatal(err)
		}
		u, err := url.Parse(source + p.ImageURL + "?width=1280&height=1280")
		if err != nil {
			t.Fatal(err)
		}
		resp, err = http.Get(u.String())
		if err != nil {
			t.Fatal(err)
		}
		data, err = io.ReadAll(resp.Body)
		resp.Body.Close()
		if err != nil {
			t.Fatal(err)
		}
		pred, err = client.predict(context.Background(), data, "")
		if err != nil {
			t.Fatal(err)
		}
		for _, f := range pred.Faces {
			if _, err = parseVector(f.Embedding); err != nil {
				t.Fatal(err)
			}
		}
		t.Logf("Real library preview returned %d faces", len(pred.Faces))
	}
}

// Exercise the real executable, including its low-priority preview child, rather
// than only mocking out decoding or testing an HTTP client in isolation.
func TestWorkerExecutableKeepsServing(t *testing.T) {
	if testing.Short() {
		t.Skip("executable integration test")
	}
	l, _, _ := fixture(t)
	entered, release := make(chan struct{}), make(chan struct{})
	var once sync.Once
	ml := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if err := r.ParseMultipartForm(8 << 20); err != nil {
			t.Error(err)
		}
		f, _, err := r.FormFile("image")
		if err != nil {
			t.Error(err)
		} else {
			if _, err := jpeg.Decode(f); err != nil {
				t.Error("worker preview not JPEG", err)
			}
			f.Close()
		}
		once.Do(func() { close(entered) })
		select {
		case <-release:
		case <-r.Context().Done():
			return
		}
		_ = json.NewEncoder(w).Encode(syntheticPrediction())
	}))
	defer ml.Close()
	binary := filepath.Join(t.TempDir(), "home-images")
	if runtime.GOOS == "windows" {
		binary += ".exe"
	}
	build := exec.Command("go", "build", "-o", binary, ".")
	if out, err := build.CombinedOutput(); err != nil {
		t.Fatalf("build: %v: %s", err, out)
	}
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	port := listener.Addr().(*net.TCPAddr).Port
	listener.Close()
	c := l.config
	c.Host = "127.0.0.1"
	c.Port = port
	c.ML = MLConfig{Enabled: true, URL: ml.URL, IntervalMS: 1000}
	c.ML.defaults()
	configPath := filepath.Join(t.TempDir(), "config.json")
	raw, _ := json.Marshal(c)
	if err = os.WriteFile(configPath, raw, 0600); err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command(binary, "--config", configPath)
	configurePreview(cmd)
	if err = cmd.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = cmd.Process.Kill(); _ = cmd.Wait() })
	base := fmt.Sprintf("http://127.0.0.1:%d", port)
	select {
	case <-entered:
	case <-time.After(25 * time.Second):
		close(release)
		t.Fatal("background worker did not reach inference")
	}
	func() {
		defer close(release)
		client := &http.Client{Timeout: 2 * time.Second}
		for _, path := range []string{"/health", "/api/next?mode=sequential", "/api/images/" + imageID("a.jpg") + "/image?width=64&height=64"} {
			resp, err := client.Get(base + path)
			if err != nil {
				t.Error("serving blocked while worker waits", err)
				continue
			}
			io.Copy(io.Discard, resp.Body)
			resp.Body.Close()
			if resp.StatusCode != 200 {
				t.Error(path, resp.Status)
			}
		}
		req, _ := http.NewRequest("POST", base+"/api/indexing/control", strings.NewReader(`{"action":"pause"}`))
		req.Header.Set("X-Home-Images", "1")
		resp, err := client.Do(req)
		if err != nil {
			t.Error(err)
		} else {
			resp.Body.Close()
			if resp.StatusCode != 200 {
				t.Error("pause during inference", resp.Status)
			}
		}
	}()
	deadline := time.Now().Add(8 * time.Second)
	for time.Now().Before(deadline) {
		resp, err := http.Get(base + "/api/indexing")
		if err == nil {
			var status struct {
				Indexed int
				Paused  bool
			}
			err = json.NewDecoder(resp.Body).Decode(&status)
			resp.Body.Close()
			if err == nil && status.Indexed == 1 && status.Paused {
				return
			}
		}
		time.Sleep(100 * time.Millisecond)
	}
	t.Fatal("worker did not persist result and pause after in-flight request")
}
