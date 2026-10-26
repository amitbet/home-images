package main

import (
	"bytes"
	"context"
	"encoding/json"
	"image"
	"image/color"
	"image/jpeg"
	"image/png"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

func fixture(t *testing.T) (*Library, *httptest.Server, string) {
	t.Helper()
	base := t.TempDir()
	root := filepath.Join(base, "photos")
	if e := os.Mkdir(root, 0755); e != nil {
		t.Fatal(e)
	}
	for _, d := range []string{"album_100%", "albumX100other", "עברית", "empty"} {
		if e := os.Mkdir(filepath.Join(root, d), 0755); e != nil {
			t.Fatal(e)
		}
	}
	for _, path := range []string{"a.jpg", "b.jpg", "album_100%/c.jpg", "albumX100other/d.jpg", "עברית/תמונה.jpg"} {
		data, e := os.ReadFile("testdata/orientation6.jpg")
		if e != nil {
			t.Fatal(e)
		}
		if e = os.WriteFile(filepath.Join(root, filepath.FromSlash(path)), data, 0644); e != nil {
			t.Fatal(e)
		}
	}
	if e := os.WriteFile(filepath.Join(root, "broken.jpg"), []byte("broken"), 0644); e != nil {
		t.Fatal(e)
	}
	l, e := newLibrary(Config{Root: root, DataDir: filepath.Join(base, "data"), ScanInterval: 30, MaxPixels: 80_000_000, FFmpeg: "ffmpeg", CORSOrigins: []string{"http://dashboard.local"}})
	if e != nil {
		t.Fatal(e)
	}
	l.Scan(context.Background())
	s := httptest.NewServer(l.Handler())
	t.Cleanup(func() { s.Close(); l.Close() })
	return l, s, root
}
func getJSON(t *testing.T, s *httptest.Server, path string, out any) int {
	t.Helper()
	response, e := s.Client().Get(s.URL + path)
	if e != nil {
		t.Fatal(e)
	}
	defer response.Body.Close()
	if out != nil {
		if e = json.NewDecoder(response.Body).Decode(out); e != nil {
			t.Fatal(e)
		}
	}
	return response.StatusCode
}
func nextPhoto(t *testing.T, s *httptest.Server, query string) Photo {
	t.Helper()
	var p Photo
	if status := getJSON(t, s, "/api/next?"+query, &p); status != 200 {
		t.Fatalf("next status %d: %+v", status, p)
	}
	return p
}

func TestMetadataAndDelivery(t *testing.T) {
	_, s, root := fixture(t)
	p := nextPhoto(t, s, "mode=sequential")
	if p.Path != "a.jpg" || p.Width != 64 || p.Height != 96 || p.TakenAt == nil || *p.TakenAt != "2020:03:04 12:13:14" || p.Exif["Make"] != "Synthetic Camera" {
		t.Fatalf("metadata: %+v", p)
	}
	if p.TakenAtOffset == nil || *p.TakenAtOffset != "+02:00" {
		t.Fatalf("offset: %+v", p.TakenAtOffset)
	}
	gps := p.Exif["GPS"].(map[string]any)
	if len(gps) == 0 {
		t.Fatal("GPS missing")
	}
	response, e := s.Client().Get(s.URL + p.ImageURL + "?width=32&height=64")
	if e != nil {
		t.Fatal(e)
	}
	defer response.Body.Close()
	data, e := io.ReadAll(response.Body)
	if e != nil {
		t.Fatal(e)
	}
	img, e := jpeg.Decode(bytes.NewReader(data))
	if e != nil {
		t.Fatal(e)
	}
	if img.Bounds().Dx() != 32 || img.Bounds().Dy() != 48 {
		t.Fatal(img.Bounds())
	}
	r, g, b, _ := img.At(1, 1).RGBA()
	if b < r || b < g {
		t.Fatal("orientation 6 must put blue at top left")
	}
	request, _ := http.NewRequest("GET", s.URL+p.ImageURL+"?width=32&height=64", nil)
	request.Header.Set("If-None-Match", response.Header.Get("ETag"))
	cached, e := s.Client().Do(request)
	if e != nil {
		t.Fatal(e)
	}
	cached.Body.Close()
	if cached.StatusCode != 304 {
		t.Fatal(cached.Status)
	}
	original, e := s.Client().Get(s.URL + p.OriginalURL)
	if e != nil {
		t.Fatal(e)
	}
	defer original.Body.Close()
	body, _ := io.ReadAll(original.Body)
	expected, _ := os.ReadFile(filepath.Join(root, "a.jpg"))
	if !bytes.Equal(body, expected) {
		t.Fatal("original was changed")
	}
	rangeRequest, _ := http.NewRequest("GET", s.URL+p.OriginalURL, nil)
	rangeRequest.Header.Set("Range", "bytes=0-15")
	part, e := s.Client().Do(rangeRequest)
	if e != nil {
		t.Fatal(e)
	}
	defer part.Body.Close()
	body, _ = io.ReadAll(part.Body)
	if part.StatusCode != 206 || !bytes.Equal(body, expected[:16]) {
		t.Fatal("range request failed")
	}
}

func TestSelection(t *testing.T) {
	_, s, _ := fixture(t)
	a := nextPhoto(t, s, "mode=sequential&recursive=false")
	b := nextPhoto(t, s, "mode=sequential&recursive=false&after="+a.ID)
	wrap := nextPhoto(t, s, "mode=sequential&recursive=false&after="+b.ID)
	if a.Path != "a.jpg" || b.Path != "b.jpg" || wrap.ID != a.ID {
		t.Fatal("sequential cursor or wrap failed")
	}
	for range 10 {
		if p := nextPhoto(t, s, "recursive=false&after="+a.ID); p.ID != b.ID {
			t.Fatal("random repeated last photo")
		}
	}
	for query, want := range map[string]int{"directory=album_100%25": 1, "directory=album_100%25/": 1, "directory=empty": 0, "q=%25": 1, "q=" + url.QueryEscape("תמונה"): 1, "q=Synthetic+Camera": 5, "recursive=false": 2} {
		var result struct {
			Total int
			Items []Photo
		}
		if status := getJSON(t, s, "/api/images?"+query, &result); status != 200 || result.Total != want {
			t.Fatalf("query %s: status %d total %d", query, status, result.Total)
		}
	}
	if getJSON(t, s, "/api/next?directory=empty", nil) != 404 {
		t.Fatal("empty directory")
	}
	for _, query := range []string{"mode=invalid", "recursive=what", "limit=0", "offset=-1"} {
		route := "/api/images?"
		if strings.HasPrefix(query, "mode") {
			route = "/api/next?"
		}
		if getJSON(t, s, route+query, nil) != 422 {
			t.Fatal(query)
		}
	}
	var dirs struct {
		Directories []string
		Total       int
	}
	getJSON(t, s, "/api/directories?limit=2", &dirs)
	if len(dirs.Directories) != 2 || dirs.Total != 4 {
		t.Fatal(dirs)
	}
}

func TestConfinementAndRescan(t *testing.T) {
	l, s, root := fixture(t)
	for _, path := range []string{"../", "D:/", "//server/share", "a.jpg:stream", "..\\"} {
		status := getJSON(t, s, "/api/images?directory="+url.QueryEscape(path), nil)
		if status != 400 && status != 403 {
			t.Fatalf("%s: %d", path, status)
		}
	}
	p := nextPhoto(t, s, "mode=sequential")
	if e := os.Remove(filepath.Join(root, "a.jpg")); e != nil {
		t.Fatal(e)
	}
	if getJSON(t, s, p.ImageURL, nil) != 404 {
		t.Fatal("deleted image still served")
	}
	l.Scan(context.Background())
	var health struct{ Images int }
	getJSON(t, s, "/health", &health)
	if health.Images != 4 {
		t.Fatal(health)
	}
	f, e := os.Create(filepath.Join(root, "b.jpg"))
	if e != nil {
		t.Fatal(e)
	}
	e = jpeg.Encode(f, image.NewRGBA(image.Rect(0, 0, 20, 10)), nil)
	f.Close()
	if e != nil {
		t.Fatal(e)
	}
	l.Scan(context.Background())
	p = nextPhoto(t, s, "recursive=false&mode=sequential")
	if p.Width != 20 {
		t.Fatalf("changed dimensions: %d", p.Width)
	}
	if e = os.WriteFile(filepath.Join(root, "b.jpg"), []byte("broken now"), 0644); e != nil {
		t.Fatal(e)
	}
	l.Scan(context.Background())
	if getJSON(t, s, "/api/next?recursive=false", nil) != 404 {
		t.Fatal("corrupt entry retained")
	}
}

func TestSymlinkAndJunctionEscape(t *testing.T) {
	l, s, root := fixture(t)
	outside := t.TempDir()
	data, _ := os.ReadFile("testdata/orientation1.jpg")
	if e := os.WriteFile(filepath.Join(outside, "secret.jpg"), data, 0644); e != nil {
		t.Fatal(e)
	}
	link := filepath.Join(root, "escape")
	if runtime.GOOS == "windows" {
		out, e := exec.Command("cmd", "/c", "mklink", "/J", link, outside).CombinedOutput()
		if e != nil {
			t.Fatalf("junction: %v %s", e, out)
		}
	} else {
		if e := os.Symlink(outside, link); e != nil {
			t.Fatal(e)
		}
	}
	t.Cleanup(func() { _ = os.Remove(link) })
	if status := getJSON(t, s, "/api/images?directory=escape", nil); status != 403 {
		t.Fatalf("junction directory status: %d", status)
	}
	if f, e := l.open("escape/secret.jpg"); e == nil {
		f.Close()
		t.Fatal("opened external file")
	}
	l.Scan(context.Background())
	var health struct{ Images int }
	getJSON(t, s, "/health", &health)
	if health.Images != 5 {
		t.Fatal(health)
	}
}

func TestOrientationAllEight(t *testing.T) {
	// Expected top-left corner for the four colored quadrants in each EXIF transform.
	colors := []color.RGBA{{255, 0, 0, 255}, {0, 128, 0, 255}, {255, 255, 0, 255}, {0, 0, 255, 255}, {255, 0, 0, 255}, {0, 0, 255, 255}, {255, 255, 0, 255}, {0, 128, 0, 255}}
	for n := 1; n <= 8; n++ {
		f, e := os.Open(filepath.Join("testdata", "orientation"+strconvItoa(n)+".jpg"))
		if e != nil {
			t.Fatal(e)
		}
		img, e := jpeg.Decode(f)
		f.Close()
		if e != nil {
			t.Fatal(e)
		}
		out := orient(img, n)
		r, g, b, _ := out.At(2, 2).RGBA()
		want := colors[n-1]
		if abs(int(r>>8)-int(want.R)) > 20 || abs(int(g>>8)-int(want.G)) > 20 || abs(int(b>>8)-int(want.B)) > 20 {
			t.Fatalf("orientation %d: %d,%d,%d", n, r>>8, g>>8, b>>8)
		}
	}
}
func strconvItoa(n int) string { return string(rune('0' + n)) }
func abs(n int) int {
	if n < 0 {
		return -n
	}
	return n
}

func TestFormatsAndPixelLimit(t *testing.T) {
	l, _, root := fixture(t)
	for _, name := range []string{"sample.bmp", "sample.gif", "sample.webp", "sample.tiff", "sample.heic"} {
		data, e := os.ReadFile(filepath.Join("testdata", name))
		if e != nil {
			t.Fatal(e)
		}
		if e = os.WriteFile(filepath.Join(root, name), data, 0644); e != nil {
			t.Fatal(e)
		}
		p, e := l.metadata(context.Background(), name)
		if e != nil {
			t.Fatalf("%s metadata: %v", name, e)
		}
		if p.Width != 96 || p.Height != 64 {
			t.Fatalf("%s dimensions: %dx%d", name, p.Width, p.Height)
		}
		if isHEIC(name) {
			continue
		}
		data, e = l.jpeg(context.Background(), p, 64, 64, 85)
		if e != nil {
			t.Fatalf("%s display: %v", name, e)
		}
		if _, e = jpeg.Decode(bytes.NewReader(data)); e != nil {
			t.Fatal(e)
		}
	}
	f, e := os.Create(filepath.Join(root, "transparent.png"))
	if e != nil {
		t.Fatal(e)
	}
	e = png.Encode(f, image.NewNRGBA(image.Rect(0, 0, 10, 10)))
	f.Close()
	if e != nil {
		t.Fatal(e)
	}
	p, e := l.metadata(context.Background(), "transparent.png")
	if e != nil {
		t.Fatal(e)
	}
	data, e := l.jpeg(context.Background(), p, 64, 64, 85)
	if e != nil {
		t.Fatal(e)
	}
	decoded, e := jpeg.Decode(bytes.NewReader(data))
	if e != nil {
		t.Fatal(e)
	}
	r, _, _, _ := decoded.At(1, 1).RGBA()
	if r < 65000 {
		t.Fatal("alpha must composite over white")
	}
	l.config.MaxPixels = 100
	if _, e = l.metadata(context.Background(), "sample.webp"); e == nil {
		t.Fatal("pixel limit not enforced")
	}
}

func TestOptionalHEIC(t *testing.T) {
	if os.Getenv("HOMEIMAGES_TEST_HEIC") == "" {
		t.Skip("Set HOMEIMAGES_TEST_HEIC=1 with FFmpeg 8.1+ to verify HEIC display")
	}
	l, _, root := fixture(t)
	data, e := os.ReadFile("testdata/sample.heic")
	if e != nil {
		t.Fatal(e)
	}
	if e = os.WriteFile(filepath.Join(root, "sample.heic"), data, 0644); e != nil {
		t.Fatal(e)
	}
	p, e := l.metadata(context.Background(), "sample.heic")
	if e != nil {
		t.Fatal(e)
	}
	data, e = l.jpeg(context.Background(), p, 64, 64, 85)
	if e != nil {
		t.Fatal(e)
	}
	if _, e = jpeg.Decode(bytes.NewReader(data)); e != nil {
		t.Fatal(e)
	}
}

func TestCORSAndLifecycle(t *testing.T) {
	l, s, _ := fixture(t)
	for origin, want := range map[string]string{"http://dashboard.local": "http://dashboard.local", "http://elsewhere.local": ""} {
		r, _ := http.NewRequest("GET", s.URL+"/health", nil)
		r.Header.Set("Origin", origin)
		resp, e := s.Client().Do(r)
		if e != nil {
			t.Fatal(e)
		}
		resp.Body.Close()
		if got := resp.Header.Get("Access-Control-Allow-Origin"); got != want {
			t.Fatal(got)
		}
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { l.Run(ctx); close(done) }()
	cancel()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("scan did not stop")
	}
	p := &program{config: Config{Root: l.rootPath, DataDir: t.TempDir(), Host: "127.0.0.1", Port: 0, ScanInterval: 30, MaxPixels: 80_000_000}}
	if e := p.Start(nil); e != nil {
		t.Fatal(e)
	}
	if e := p.Stop(nil); e != nil {
		t.Fatal(e)
	}
}
