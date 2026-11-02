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
	"strings"
	"testing"
	"time"
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
	cmd := exec.Command(ffmpeg, "-v", "error", "-f", "lavfi", "-i", "color=c=red:s=64x64:r=5", "-t", "1", "-c:v", "mjpeg", "-y", path)
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
	key, err := l.previewKey(Photo{Path: "old.MOV"}, "image:480:360:72")
	if err != nil || !strings.HasPrefix(key, "video-") || !l.thumbnails.exists(key) {
		t.Fatalf("video thumbnail not saved in persistent cache: key=%q err=%v", key, err)
	}
	resp, err := s.Client().Get(s.URL + "/api/images/" + imageID("old.MOV") + "/video")
	if err != nil {
		t.Fatal(err)
	}
	data, err := io.ReadAll(resp.Body)
	resp.Body.Close()
	if err != nil || resp.StatusCode != 200 || resp.Header.Get("Content-Type") != "video/mp4" || !bytes.Contains(data, []byte("moov")) {
		t.Fatalf("conversion: status=%d bytes=%d err=%v", resp.StatusCode, len(data), err)
	}
	// The index must come first and byte ranges must work, or the player cannot seek.
	if moov, mdat := bytes.Index(data, []byte("moov")), bytes.Index(data, []byte("mdat")); mdat < 0 || moov > mdat {
		t.Fatalf("converted video is not faststart: moov=%d mdat=%d", moov, mdat)
	}
	req, _ := http.NewRequest("GET", s.URL+"/api/images/"+imageID("old.MOV")+"/video", nil)
	req.Header.Set("Range", "bytes=10-19")
	ranged, err := s.Client().Do(req)
	if err != nil {
		t.Fatal(err)
	}
	part, _ := io.ReadAll(ranged.Body)
	ranged.Body.Close()
	if ranged.StatusCode != http.StatusPartialContent || !bytes.Equal(part, data[10:20]) {
		t.Fatalf("converted range: %d %q", ranged.StatusCode, part)
	}
	decode := exec.Command(ffmpeg, "-v", "error", "-i", "pipe:0", "-frames:v", "1", "-f", "null", "-")
	decode.Stdin = bytes.NewReader(data)
	if output, err := decode.CombinedOutput(); err != nil {
		t.Fatalf("converted video cannot be decoded: %v: %s", err, output)
	}
}

func TestVideoCaptureDate(t *testing.T) {
	ffmpeg, err := exec.LookPath("ffmpeg")
	if err != nil {
		t.Skip("FFmpeg not installed")
	}
	l, s, root := fixture(t)
	path := filepath.Join(root, "PICT0134.MOV")
	cmd := exec.Command(ffmpeg, "-v", "error", "-f", "lavfi", "-i", "color=c=blue:s=320x240:r=5", "-t", "1", "-c:v", "mjpeg",
		"-metadata", "creation_time=2005-07-15T10:23:45Z", "-y", path)
	if output, err := cmd.CombinedOutput(); err != nil {
		t.Skipf("FFmpeg fixture unavailable: %v: %s", err, output)
	}
	check := func(stage string) {
		var p Photo
		if status := getJSON(t, s, "/api/images/"+imageID("PICT0134.MOV"), &p); status != 200 {
			t.Fatalf("%s: status %d", stage, status)
		}
		if p.TakenAt == nil || *p.TakenAt != "2005:07:15 10:23:45" || p.Width != 320 || p.Height != 240 {
			t.Fatalf("%s: taken=%v size=%dx%d", stage, p.TakenAt, p.Width, p.Height)
		}
	}
	l.Scan(context.Background())
	check("first scan")
	// Videos indexed before FFprobe was used have no probe marker; the next
	// scan must re-read them even though the file itself is unchanged.
	if _, err = l.db.Exec(`UPDATE images SET metadata=metadata_pack(json_remove(json_remove(json_set(metadata_json(metadata),'$.width',0,'$.height',0),'$.taken_at'),'$.exif.video_probe')) WHERE path='PICT0134.MOV'`); err != nil {
		t.Fatal(err)
	}
	l.Scan(context.Background())
	check("backfill scan")
	// Samsung: UTC creation_time plus the phone's offset.
	phone := filepath.Join(root, "20260512_201758.mp4")
	cmd = exec.Command(ffmpeg, "-v", "error", "-f", "lavfi", "-i", "color=c=blue:s=64x48:r=5", "-t", "1", "-c:v", "mjpeg",
		"-metadata", "creation_time=2026-05-12T17:18:20Z", "-metadata", "com.samsung.android.utc_offset=+0300", "-movflags", "use_metadata_tags", "-y", phone)
	if output, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("phone fixture: %v: %s", err, output)
	}
	l.Scan(context.Background())
	var p Photo
	getJSON(t, s, "/api/images/"+imageID("20260512_201758.mp4"), &p)
	if p.TakenAt == nil || *p.TakenAt != "2026:05:12 20:18:20" || p.TakenAtOffset == nil || *p.TakenAtOffset != "+03:00" {
		t.Fatalf("samsung video: taken=%v offset=%v", p.TakenAt, p.TakenAtOffset)
	}
}

func TestVideoCaptureTime(t *testing.T) {
	plus3 := time.FixedZone("", 3*3600)
	for _, tc := range []struct {
		raw           string
		zone          *time.Location
		taken, offset string
	}{
		{"2005-07-15T10:23:45.000000Z", nil, "2005:07:15 10:23:45", ""},
		{"2023-05-01T12:34:56+0300", nil, "2023:05:01 12:34:56", "+03:00"},
		{"2026-05-12T17:18:20.000000Z", plus3, "2026:05:12 20:18:20", "+03:00"},
		{"1904-01-01T00:00:00.000000Z", nil, "", ""},
		{"1970-01-01T00:00:00.000000Z", nil, "", ""},
		{"garbage", nil, "", ""},
	} {
		taken, offset, ok := videoCaptureTime(tc.raw, tc.zone)
		if taken != tc.taken || offset != tc.offset || ok != (tc.taken != "") {
			t.Errorf("%q: got %q %q %t", tc.raw, taken, offset, ok)
		}
	}
}
