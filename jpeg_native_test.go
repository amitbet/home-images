//go:build (windows || linux || darwin) && (amd64 || arm64)

package main

import (
	"bytes"
	"context"
	"encoding/json"
	"image/color"
	"image/jpeg"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"testing"
	"time"
)

func testTurboLibrary(t *testing.T) *turboJPEG {
	t.Helper()
	path := testTurboPath()
	if path == "" {
		t.Skip("set HOMEIMAGES_TEST_TURBO_LIBRARY to test native codec")
	}
	lib, err := loadTurboJPEG(path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { lib.close() })
	return lib
}
func TestNativeJPEGOrientationLimitsAndProgressive(t *testing.T) {
	lib := testTurboLibrary(t)
	colors := []color.RGBA{{255, 0, 0, 255}, {0, 128, 0, 255}, {255, 255, 0, 255}, {0, 0, 255, 255}, {255, 0, 0, 255}, {0, 0, 255, 255}, {255, 255, 0, 255}, {0, 128, 0, 255}}
	for n := 1; n <= 8; n++ {
		f, err := os.Open(filepath.Join("testdata", "orientation"+strconvItoa(n)+".jpg"))
		if err != nil {
			t.Fatal(err)
		}
		data, err := lib.preview(context.Background(), f, Photo{Orientation: n}, 480, 360, 85, 80_000_000)
		f.Close()
		if err != nil {
			t.Fatal(n, err)
		}
		im, err := jpeg.Decode(bytes.NewReader(data))
		if err != nil {
			t.Fatal(err)
		}
		r, g, b, _ := im.At(2, 2).RGBA()
		want := colors[n-1]
		if abs(int(r>>8)-int(want.R)) > 25 || abs(int(g>>8)-int(want.G)) > 25 || abs(int(b>>8)-int(want.B)) > 25 {
			t.Fatalf("native orientation %d: %d,%d,%d", n, r>>8, g>>8, b>>8)
		}
	}
	f, err := os.Open("testdata/preview-progressive.jpg")
	if err != nil {
		t.Fatal(err)
	}
	data, err := lib.preview(context.Background(), f, Photo{Orientation: 1}, 32, 32, 72, 80_000_000)
	f.Close()
	if err != nil {
		t.Fatal(err)
	}
	cfg, err := jpeg.DecodeConfig(bytes.NewReader(data))
	if err != nil || cfg.Width > 32 || cfg.Height > 32 {
		t.Fatal(cfg, err)
	}
	f, err = os.Open("testdata/orientation1.jpg")
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	if _, err = lib.preview(context.Background(), f, Photo{}, 480, 360, 72, 1); err == nil {
		t.Fatal("native pixel limit bypassed")
	}
	original, err := os.ReadFile("testdata/orientation1.jpg")
	if err != nil {
		t.Fatal(err)
	}
	truncated := filepath.Join(t.TempDir(), "truncated.jpg")
	if err = os.WriteFile(truncated, original[:len(original)-20], 0600); err != nil {
		t.Fatal(err)
	}
	broken, err := os.Open(truncated)
	if err != nil {
		t.Fatal(err)
	}
	_, err = lib.preview(context.Background(), broken, Photo{Orientation: 1}, 480, 360, 72, 80_000_000)
	broken.Close()
	if err == nil {
		t.Fatal("native decoder accepted truncated JPEG warning")
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err = lib.preview(ctx, f, Photo{}, 480, 360, 72, 80_000_000); err != context.Canceled {
		t.Fatal("cancellation", err)
	}
	if _, err = loadTurboJPEG("turbojpeg.dll"); err == nil {
		t.Fatal("relative DLL search allowed")
	}
}
func TestNativeJPEGWorkerFallback(t *testing.T) {
	lib := testTurboLibrary(t)
	source := os.Getenv("HOMEIMAGES_TEST_PREVIEW_EXE")
	if source == "" {
		t.Skip("set HOMEIMAGES_TEST_PREVIEW_EXE for isolated worker test")
	}
	base := t.TempDir()
	name := "home-images"
	if runtime.GOOS == "windows" {
		name += ".exe"
	}
	app := filepath.Join(base, name)
	dll := filepath.Join(base, turboLibraryName())
	for dst, src := range map[string]string{app: source, dll: testTurboPath()} {
		data, err := os.ReadFile(src)
		if err != nil {
			t.Fatal(err)
		}
		if err = os.WriteFile(dst, data, 0755); err != nil {
			t.Fatal(err)
		}
	}
	root, _ := filepath.Abs("testdata")
	run := func(decoder, photo string, crop ...*faceCrop) []byte {
		t.Helper()
		ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()
		req := previewRequest{Config: Config{Root: root, MaxPixels: 80_000_000, JPEGDecoder: decoder}, Width: 480, Height: 360, Quality: 85}
		if len(crop) > 0 {
			req.FaceCrop = crop[0]
		}
		data, _ := json.Marshal(req)
		cmd := exec.CommandContext(ctx, app, "--ai-preview", photo)
		cmd.Stdin = bytes.NewReader(data)
		var stderr bytes.Buffer
		cmd.Stderr = &stderr
		output, err := cmd.Output()
		if err != nil {
			t.Fatal(err, stderr.String())
		}
		return output
	}
	pure := run("go", "orientation6.jpg")
	accelerated := run("auto", "orientation6.jpg")
	f, err := os.Open(filepath.Join(root, "orientation6.jpg"))
	if err != nil {
		t.Fatal(err)
	}
	expected, err := lib.preview(context.Background(), f, Photo{Orientation: 6}, 480, 360, 85, 80_000_000)
	f.Close()
	if err != nil {
		t.Fatal(err)
	}
	crop := &faceCrop{Box: faceBox{X1: 0, Y1: 0, X2: 50, Y2: 100}, Width: 100, Height: 100}
	f, err = os.Open(filepath.Join(root, "orientation6.jpg"))
	if err != nil {
		t.Fatal(err)
	}
	faceExpected, err := lib.preview(context.Background(), f, Photo{Orientation: 6}, 480, 360, 85, 80_000_000, crop)
	f.Close()
	if err != nil {
		t.Fatal(err)
	}
	faceNative := run("auto", "orientation6.jpg", crop)
	if !bytes.Equal(faceExpected, faceNative) {
		t.Fatal("worker did not use native face crop")
	}
	for _, data := range [][]byte{faceNative, run("go", "orientation6.jpg", crop)} {
		cfg, e := jpeg.DecodeConfig(bytes.NewReader(data))
		if e != nil || cfg.Width != 160 || cfg.Height != 160 {
			t.Fatal("invalid worker face crop", cfg, e)
		}
	}
	if !bytes.Equal(expected, accelerated) {
		t.Fatal("worker did not use native codec")
	}
	if runtime.GOOS != "windows" {
		alias := filepath.Join(t.TempDir(), "home-images")
		if err := os.Symlink(app, alias); err != nil {
			t.Fatal(err)
		}
		original := app
		app = alias
		if !bytes.Equal(expected, run("auto", "orientation6.jpg")) {
			t.Fatal("symlinked executable did not locate its bundled codec")
		}
		app = original
	}
	if !bytes.Equal(run("go", "preview-cmyk.jpg"), run("auto", "preview-cmyk.jpg")) {
		t.Fatal("unsupported CMYK did not fall back to Go")
	}
	if err = os.Remove(dll); err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(pure, run("auto", "orientation6.jpg")) {
		t.Fatal("missing DLL did not fall back to Go")
	}
	if err = os.WriteFile(dll, []byte("not a DLL"), 0600); err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(pure, run("auto", "orientation6.jpg")) {
		t.Fatal("unloadable DLL did not fall back to Go")
	}
}

func testTurboPath() string {
	if path := os.Getenv("HOMEIMAGES_TEST_TURBO_LIBRARY"); path != "" {
		return path
	}
	return os.Getenv("HOMEIMAGES_TEST_TURBO_DLL")
}
