package main

import (
	"bytes"
	"context"
	"image/jpeg"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

func TestVideoBrowsingAndFrameIsolation(t *testing.T) {
	l, s, root := fixture(t)
	content := []byte("test video bytes for range requests")
	if err := os.WriteFile(filepath.Join(root, "0.MP4"), content, 0644); err != nil {
		t.Fatal(err)
	}
	l.Scan(context.Background())
	for _, tc := range []struct {
		query                string
		wantVideo, wantImage bool
	}{
		{"", true, true}, {"&media_type=videos", true, false}, {"&media_type=images", false, true},
	} {
		var result struct {
			Items []Photo `json:"items"`
		}
		status := getJSON(t, s, "/api/search?mode=metadata&limit=100"+tc.query, &result)
		var video, photo bool
		for _, item := range result.Items {
			if item.Format == "VIDEO" {
				video = true
			} else {
				photo = true
			}
		}
		if status != 200 || video != tc.wantVideo || photo != tc.wantImage {
			t.Fatalf("search %q: status %d, video=%t photo=%t", tc.query, status, video, photo)
		}
	}
	var groups struct {
		Total int `json:"total"`
	}
	if status := getJSON(t, s, "/api/search?mode=metadata&media_type=videos&group_by=directory&group_view=folders", &groups); status != 200 || groups.Total == 0 {
		t.Fatalf("video groups: %d %+v", status, groups)
	}
	var p Photo
	if status := getJSON(t, s, "/api/images/"+imageID("0.MP4"), &p); status != 200 || p.Format != "VIDEO" {
		t.Fatalf("video metadata: %d %+v", status, p)
	}
	req, _ := http.NewRequest("GET", s.URL+p.OriginalURL, nil)
	req.Header.Set("Range", "bytes=0-3")
	resp, err := s.Client().Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusPartialContent || string(body) != string(content[:4]) {
		t.Fatalf("range: %d %q", resp.StatusCode, body)
	}
	thumb, err := s.Client().Get(s.URL + "/api/images/" + p.ID + "/thumbnail")
	if err != nil {
		t.Fatal(err)
	}
	thumb.Body.Close()
	if thumb.StatusCode != 200 {
		t.Fatalf("thumbnail: %d", thumb.StatusCode)
	}
	if got := nextPhoto(t, s, "mode=sequential"); got.Format == "VIDEO" {
		t.Fatal("video returned to picture frame")
	}
	var frame Photo
	if status := getJSON(t, s, "/api/playback/next?mode=metadata&order=sequential", &frame); status != 200 || frame.Format == "VIDEO" {
		t.Fatalf("playback: %d %+v", status, frame)
	}
}

func TestVideoConversion(t *testing.T) {
	ffmpeg, err := exec.LookPath("ffmpeg")
	if err != nil {
		t.Skip("FFmpeg not installed")
	}
	l, s, root := fixture(t)
	path := filepath.Join(root, "old.MOV")
	cmd := exec.Command(ffmpeg, "-v", "error", "-f", "lavfi", "-i", "color=c=red:s=64x64:r=5", "-t", "1", "-c:v", "mpeg4", "-y", path)
	if output, err := cmd.CombinedOutput(); err != nil {
		t.Skipf("FFmpeg fixture unavailable: %v: %s", err, output)
	}
	l.Scan(context.Background())
	thumb, err := s.Client().Get(s.URL + "/api/images/" + imageID("old.MOV") + "/thumbnail")
	if err != nil {
		t.Fatal(err)
	}
	img, err := jpeg.Decode(thumb.Body)
	thumb.Body.Close()
	if err != nil || thumb.StatusCode != 200 {
		t.Fatalf("video thumbnail: %d %v", thumb.StatusCode, err)
	}
	red, green, blue, _ := img.At(img.Bounds().Dx()/2, img.Bounds().Dy()/2).RGBA()
	if red < 30000 || red < green*2 || red < blue*2 {
		t.Fatalf("thumbnail does not show red video frame: %d %d %d", red, green, blue)
	}
	resp, err := s.Client().Get(s.URL + "/api/images/" + imageID("old.MOV") + "/video")
	if err != nil {
		t.Fatal(err)
	}
	data, err := io.ReadAll(resp.Body)
	resp.Body.Close()
	if err != nil || resp.StatusCode != 200 || resp.Header.Get("Content-Type") != "video/mp4" || !bytes.Contains(data, []byte("moov")) || !bytes.Contains(data, []byte("moof")) {
		t.Fatalf("conversion: status=%d bytes=%d err=%v", resp.StatusCode, len(data), err)
	}
	decode := exec.Command(ffmpeg, "-v", "error", "-i", "pipe:0", "-frames:v", "1", "-f", "null", "-")
	decode.Stdin = bytes.NewReader(data)
	if output, err := decode.CombinedOutput(); err != nil {
		t.Fatalf("converted video cannot be decoded: %v: %s", err, output)
	}
}
