package main

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"log"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"time"
)

// Converted videos are kept so repeat views start instantly; the oldest
// conversions are dropped once the cache grows past this size.
const videoCacheLimit = 4 << 30

type videoFlight struct {
	done chan struct{}
	err  error
}

func (l *Library) videoCacheDir() string {
	return filepath.Join(l.config.DataDir, "video-cache-v1")
}

// videoPlayback converts formats the browser cannot decode into a complete,
// faststart MP4 and serves it with range support so the player can seek.
// The original remains untouched.
func (l *Library) videoPlayback(w http.ResponseWriter, r *http.Request) error {
	p, err := l.get(r.Context(), r.PathValue("id"))
	if err != nil {
		return err
	}
	if !isVideo(p.Path) {
		return bad(415, "Not a video")
	}
	ffmpeg, err := exec.LookPath(l.config.FFmpeg)
	if err != nil {
		return bad(503, "Video conversion requires FFmpeg")
	}
	key, err := l.previewKey(p, "playback-mp4-v1")
	if err != nil {
		return err
	}
	path := filepath.Join(l.videoCacheDir(), key[:2], key+".mp4")
	if r.Method == http.MethodHead {
		w.Header().Set("Content-Type", "video/mp4")
		return nil
	}
	// Converting a long video can outlast the server's write timeout.
	_ = http.NewResponseController(w).SetWriteDeadline(time.Time{})
	if err = l.convertVideo(r.Context(), ffmpeg, p.AbsolutePath, path); err != nil {
		if r.Context().Err() != nil {
			return nil
		}
		log.Printf("video conversion %s: %v", p.Path, err)
		return bad(422, "Video conversion failed")
	}
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil {
		return err
	}
	now := time.Now()
	_ = os.Chtimes(path, now, now) // keeps recently watched videos in the cache
	w.Header().Set("Content-Type", "video/mp4")
	w.Header().Set("Cache-Control", "private, max-age=3600")
	http.ServeContent(w, r, filepath.Base(path), info.ModTime(), f)
	return nil
}

// convertVideo makes sure the converted file exists. Concurrent requests for
// the same video (the player issues several range requests) share one FFmpeg run.
func (l *Library) convertVideo(ctx context.Context, ffmpeg, source, target string) error {
	for {
		if info, err := os.Stat(target); err == nil && info.Size() > 0 {
			return nil
		}
		l.videoMu.Lock()
		if l.videoFlights == nil {
			l.videoFlights = map[string]*videoFlight{}
		}
		flight, running := l.videoFlights[target]
		if !running {
			flight = &videoFlight{done: make(chan struct{})}
			l.videoFlights[target] = flight
		}
		l.videoMu.Unlock()
		if !running {
			flight.err = l.runVideoConversion(ctx, ffmpeg, source, target)
			l.videoMu.Lock()
			delete(l.videoFlights, target)
			l.videoMu.Unlock()
			close(flight.done)
			return flight.err
		}
		select {
		case <-flight.done:
		case <-ctx.Done():
			return ctx.Err()
		}
		// The owner may have been cancelled by its own client disconnecting;
		// in that case this request starts the conversion itself.
		if flight.err != nil && !errors.Is(flight.err, context.Canceled) {
			return flight.err
		}
	}
}

func (l *Library) runVideoConversion(ctx context.Context, ffmpeg, source, target string) error {
	select {
	case l.videoSlots <- struct{}{}:
		defer func() { <-l.videoSlots }()
	case <-ctx.Done():
		return ctx.Err()
	}
	if err := os.MkdirAll(filepath.Dir(target), 0750); err != nil {
		return err
	}
	tmp := fmt.Sprintf("%s.%d.tmp", target, time.Now().UnixNano())
	defer os.Remove(tmp)
	cmd := exec.CommandContext(ctx, ffmpeg, "-nostdin", "-v", "error", "-i", source, "-map", "0:v:0", "-map", "0:a:0?",
		"-vf", "pad=ceil(iw/2)*2:ceil(ih/2)*2", "-c:v", "libx264", "-preset", "veryfast", "-pix_fmt", "yuv420p",
		"-c:a", "aac", "-movflags", "+faststart", "-f", "mp4", "-y", tmp)
	if output, err := cmd.CombinedOutput(); err != nil {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		return fmt.Errorf("%w: %s", err, limitOutput(output))
	}
	if info, err := os.Stat(tmp); err != nil || info.Size() == 0 {
		return fmt.Errorf("FFmpeg produced no output")
	}
	if err := os.Rename(tmp, target); err != nil {
		return err
	}
	l.trimVideoCache(target)
	return nil
}

func limitOutput(b []byte) []byte {
	if len(b) > 500 {
		return b[len(b)-500:]
	}
	return b
}

// trimVideoCache removes the least recently watched conversions beyond the limit.
func (l *Library) trimVideoCache(keep string) {
	type entry struct {
		path string
		size int64
		used time.Time
	}
	var entries []entry
	var total int64
	_ = filepath.WalkDir(l.videoCacheDir(), func(path string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() || filepath.Ext(path) != ".mp4" {
			return nil
		}
		if info, e := d.Info(); e == nil {
			entries = append(entries, entry{path, info.Size(), info.ModTime()})
			total += info.Size()
		}
		return nil
	})
	sort.Slice(entries, func(i, j int) bool { return entries[i].used.Before(entries[j].used) })
	for _, e := range entries {
		if total <= videoCacheLimit {
			break
		}
		if e.path == keep {
			continue
		}
		if os.Remove(e.path) == nil {
			total -= e.size
		}
	}
}
