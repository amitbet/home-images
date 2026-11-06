package main

import (
	"bytes"
	"context"
	"image/jpeg"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

func TestVideoPreviewCoverAndFrame(t *testing.T) {
	ffmpeg, err := exec.LookPath("ffmpeg")
	if err != nil {
		t.Skip("FFmpeg not installed")
	}
	run := func(args ...string) {
		t.Helper()
		if out, err := exec.Command(ffmpeg, args...).CombinedOutput(); err != nil {
			t.Fatalf("ffmpeg: %v: %s", err, out)
		}
	}
	dir := t.TempDir()
	video := filepath.Join(dir, "plain.mp4")
	cover := filepath.Join(dir, "cover.jpg")
	attached := filepath.Join(dir, "attached.mp4")
	run("-v", "error", "-f", "lavfi", "-i", "color=c=blue:s=160x120:d=0.1", "-c:v", "mpeg4", video)
	run("-v", "error", "-f", "lavfi", "-i", "color=c=red:s=160x120", "-frames:v", "1", cover)
	run("-v", "error", "-i", video, "-i", cover, "-map", "0", "-map", "1", "-c", "copy", "-disposition:v:1", "attached_pic", attached)
	l := &Library{config: Config{FFmpeg: ffmpeg}}
	for _, tc := range []struct {
		path string
		red  bool
	}{{video, false}, {attached, true}} {
		data, err := l.videoPreview(context.Background(), tc.path, 80, 60, 72)
		if err != nil {
			t.Fatal(err)
		}
		img, err := jpeg.Decode(bytes.NewReader(data))
		if err != nil {
			t.Fatal(err)
		}
		if img.Bounds().Dx() != 80 || img.Bounds().Dy() != 60 {
			t.Fatal(img.Bounds())
		}
		r, _, b, _ := img.At(40, 30).RGBA()
		if (r > b) != tc.red {
			t.Fatalf("wrong source for %s: r=%d b=%d", tc.path, r, b)
		}
	}
	if err := os.WriteFile(filepath.Join(dir, "broken.mp4"), []byte("bad"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := l.videoPreview(context.Background(), filepath.Join(dir, "broken.mp4"), 80, 60, 72); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := l.videoPreview(ctx, video, 80, 60, 72); err != context.Canceled {
		t.Fatalf("cancellation: %v", err)
	}
}
