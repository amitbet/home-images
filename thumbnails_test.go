package main

import (
	"bytes"
	"context"
	"image"
	"image/color"
	"image/jpeg"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func testJPEG(t *testing.T, w, h int) []byte {
	t.Helper()
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			img.Set(x, y, color.RGBA{uint8(x), uint8(y), 120, 255})
		}
	}
	var buf bytes.Buffer
	if err := jpeg.Encode(&buf, img, &jpeg.Options{Quality: 75}); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}
func TestThumbnailCacheSingleFlightAndPersistence(t *testing.T) {
	cache := newThumbnailCache(t.TempDir())
	key := strings.Repeat("a", 64)
	data := testJPEG(t, 64, 48)
	var builds atomic.Int32
	build := func() ([]byte, error) { builds.Add(1); time.Sleep(20 * time.Millisecond); return data, nil }
	var wg sync.WaitGroup
	failures := make(chan error, 16)
	for range 16 {
		wg.Go(func() {
			got, err := cache.get(context.Background(), key, build)
			if err != nil {
				failures <- err
			} else if !bytes.Equal(got, data) {
				t.Error("different thumbnail bytes")
			}
		})
	}
	wg.Wait()
	close(failures)
	for err := range failures {
		t.Fatal(err)
	}
	if builds.Load() != 1 {
		t.Fatalf("decoded %d times instead of once", builds.Load())
	}
	reopened := newThumbnailCache(filepath.Dir(cache.root))
	if _, err := reopened.get(context.Background(), key, build); err != nil {
		t.Fatal(err)
	}
	if builds.Load() != 1 {
		t.Fatal("persistent cache was not reused")
	}
}
func TestThumbnailEndpointAndInvalidation(t *testing.T) {
	l, s, root := fixture(t)
	job := testJob(t, l, "a.jpg")
	p, err := l.get(context.Background(), job.ID)
	if err != nil {
		t.Fatal(err)
	}
	url := s.URL + "/api/images/" + p.ID + "/thumbnail"
	get := func() (int, string, []byte) {
		t.Helper()
		res, err := s.Client().Get(url)
		if err != nil {
			t.Fatal(err)
		}
		defer res.Body.Close()
		data, _ := io.ReadAll(res.Body)
		return res.StatusCode, res.Header.Get("ETag"), data
	}
	status, etag, data := get()
	if status != 200 {
		t.Fatal(status, string(data))
	}
	cfg, err := jpeg.DecodeConfig(bytes.NewReader(data))
	if err != nil || cfg.Width > 480 || cfg.Height > 360 {
		t.Fatal(cfg, err)
	}
	key, err := l.previewKey(p, "image:480:360:72")
	if err != nil {
		t.Fatal(err)
	}
	cached, ok := l.thumbnails.read(key)
	if !ok || !bytes.Equal(cached, data) {
		t.Fatal("thumbnail not persisted")
	}
	req, _ := http.NewRequest("GET", url, nil)
	req.Header.Set("If-None-Match", etag)
	res, err := s.Client().Do(req)
	if err != nil {
		t.Fatal(err)
	}
	res.Body.Close()
	if res.StatusCode != 304 {
		t.Fatal("conditional thumbnail", res.StatusCode)
	}
	if err = os.WriteFile(filepath.Join(root, "a.jpg"), testJPEG(t, 800, 400), 0644); err != nil {
		t.Fatal(err)
	}
	status, newETag, newData := get()
	cfg, err = jpeg.DecodeConfig(bytes.NewReader(newData))
	if status != 200 || err != nil || newETag == etag || cfg.Width != 480 || cfg.Height != 240 {
		t.Fatal("stale thumbnail after source change", status, cfg, err)
	}
	if err = os.Remove(filepath.Join(root, "a.jpg")); err != nil {
		t.Fatal(err)
	}
	status, _, _ = get()
	if status != 404 {
		t.Fatal("served cached deleted original", status)
	}
}
func TestThumbnailSeedingAndFaceCache(t *testing.T) {
	l, s, _ := fixture(t)
	job := testJob(t, l, "a.jpg")
	p, err := l.get(context.Background(), job.ID)
	if err != nil {
		t.Fatal(err)
	}
	key, _ := l.previewKey(p, "image:480:360:72")
	preview, err := l.renderJPEG(context.Background(), p, 1280, 1280, 80)
	if err != nil {
		t.Fatal(err)
	}
	l.seedThumbnail(p, preview, key)
	if _, ok := l.thumbnails.read(key); !ok {
		t.Fatal("ML preview did not seed gallery thumbnail")
	}
	if err = l.ai.save(context.Background(), job, syntheticPrediction()); err != nil {
		t.Fatal(err)
	}
	var groups struct{ Items []personSummary }
	getJSON(t, s, "/api/people", &groups)
	if len(groups.Items) != 1 {
		t.Fatal(groups)
	}
	res, err := s.Client().Get(s.URL + groups.Items[0].ImageURL)
	if err != nil {
		t.Fatal(err)
	}
	data, _ := io.ReadAll(res.Body)
	res.Body.Close()
	if res.StatusCode != 200 {
		t.Fatal(res.StatusCode, string(data))
	}
	etag := res.Header.Get("ETag")
	if etag == "" {
		t.Fatal("portrait has no validator")
	}
	request, _ := http.NewRequest("GET", s.URL+groups.Items[0].ImageURL, nil)
	request.Header.Set("If-None-Match", etag)
	validated, err := s.Client().Do(request)
	if err != nil {
		t.Fatal(err)
	}
	validated.Body.Close()
	if validated.StatusCode != http.StatusNotModified {
		t.Fatal("portrait revalidation", validated.StatusCode)
	}
	cfg, err := jpeg.DecodeConfig(bytes.NewReader(data))
	if err != nil || cfg.Width != 160 || cfg.Height != 160 {
		t.Fatal(cfg, err)
	}
	var cachedFaces int
	_ = filepath.WalkDir(l.thumbnails.root, func(path string, d os.DirEntry, err error) error {
		if err == nil && !d.IsDir() && strings.HasSuffix(path, ".jpg") {
			cachedFaces++
		}
		return nil
	})
	if cachedFaces < 2 {
		t.Fatal("face portrait not persisted")
	}
}
func TestThumbnailPruningOnlyGeneratedFiles(t *testing.T) {
	cache := newThumbnailCache(t.TempDir())
	if err := os.MkdirAll(cache.root, 0755); err != nil {
		t.Fatal(err)
	}
	for _, char := range []string{"a", "b", "c"} {
		key := strings.Repeat(char, 64)
		if err := os.MkdirAll(filepath.Dir(cache.path(key)), 0755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(cache.path(key), make([]byte, 10), 0644); err != nil {
			t.Fatal(err)
		}
	}
	keep := filepath.Join(cache.root, "keep.txt")
	if err := os.WriteFile(keep, []byte("keep"), 0644); err != nil {
		t.Fatal(err)
	}
	cache.prune(16)
	if _, err := os.Stat(keep); err != nil {
		t.Fatal("removed unrelated file", err)
	}
	var total int64
	_ = filepath.WalkDir(cache.root, func(path string, d os.DirEntry, err error) error {
		if err == nil && !d.IsDir() && strings.HasSuffix(path, ".jpg") {
			i, _ := d.Info()
			total += i.Size()
		}
		return nil
	})
	if total > 16 {
		t.Fatal("cache still over budget", total)
	}
}

func TestViewerPreviewCacheAndInvalidation(t *testing.T) {
	l, server, root := fixture(t)
	job := testJob(t, l, "a.jpg")
	p, err := l.get(context.Background(), job.ID)
	if err != nil {
		t.Fatal(err)
	}
	url := server.URL + "/api/images/" + p.ID + "/image?width=1920&height=1440"
	get := func() []byte {
		t.Helper()
		res, err := server.Client().Get(url)
		if err != nil {
			t.Fatal(err)
		}
		defer res.Body.Close()
		data, _ := io.ReadAll(res.Body)
		if res.StatusCode != 200 {
			t.Fatalf("%d: %s", res.StatusCode, data)
		}
		return data
	}
	first := get()
	key, err := l.previewKey(p, "image:1920:1440:85")
	if err != nil {
		t.Fatal(err)
	}
	if cached, ok := l.thumbnails.read(key); !ok || !bytes.Equal(cached, first) {
		t.Fatal("viewer preview not cached")
	}
	// A cache hit must not need a decoder/render slot.
	l.renderSlots = make(chan struct{}, 1)
	l.renderSlots <- struct{}{}
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	second, err := l.jpeg(ctx, p, 1920, 1440, 85)
	<-l.renderSlots
	if err != nil || !bytes.Equal(first, second) {
		t.Fatal("cached viewer used renderer", err)
	}
	if err := os.WriteFile(filepath.Join(root, "a.jpg"), testJPEG(t, 900, 450), 0644); err != nil {
		t.Fatal(err)
	}
	changed := get()
	config, err := jpeg.DecodeConfig(bytes.NewReader(changed))
	if err != nil || config.Width != 900 || config.Height != 450 || bytes.Equal(first, changed) {
		t.Fatal("stale viewer preview", config, err)
	}
}
func TestCachedThumbnailDoesNotPauseIndexing(t *testing.T) {
	l, s, _ := fixture(t)
	url := s.URL + "/api/images/" + imageID("a.jpg") + "/thumbnail"
	first, err := s.Client().Get(url)
	if err != nil {
		t.Fatal(err)
	}
	io.Copy(io.Discard, first.Body)
	first.Body.Close()
	if first.StatusCode != 200 || l.lastRender.Load() == 0 {
		t.Fatal("cold render did not signal foreground work")
	}
	l.lastRender.Store(0)
	second, err := s.Client().Get(url)
	if err != nil {
		t.Fatal(err)
	}
	io.Copy(io.Discard, second.Body)
	second.Body.Close()
	if second.StatusCode != 200 || l.lastRender.Load() != 0 {
		t.Fatal("cached thumbnail incorrectly paused indexing")
	}
}

func TestViewerPreviewVariantCached(t *testing.T) {
	l, _, _ := fixture(t)
	ctx := context.Background()
	j := testJob(t, l, "a.jpg")
	p, e := l.get(ctx, j.ID)
	if e != nil {
		t.Fatal(e)
	}
	data, e := l.jpeg(ctx, p, 1280, 1280, 80)
	if e != nil {
		t.Fatal(e)
	}
	key, e := l.previewKey(p, "image:1280:1280:80")
	if e != nil {
		t.Fatal(e)
	}
	cached, ok := l.thumbnails.read(key)
	if !ok || !bytes.Equal(data, cached) {
		t.Fatal("viewer preview was not cached")
	}
	again, e := l.jpeg(ctx, p, 1280, 1280, 80)
	if e != nil || !bytes.Equal(data, again) {
		t.Fatal("viewer cache not reused", e)
	}
}
