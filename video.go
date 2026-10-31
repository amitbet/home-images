package main

import (
	"io"
	"log"
	"net/http"
	"os/exec"
)

// videoPlayback converts formats the browser cannot decode into a fragmented
// MP4 stream. The original remains untouched; disconnecting stops FFmpeg.
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
	if r.Method == http.MethodHead {
		w.Header().Set("Content-Type", "video/mp4")
		return nil
	}
	select {
	case l.videoSlots <- struct{}{}:
		defer func() { <-l.videoSlots }()
	case <-r.Context().Done():
		return nil
	}
	cmd := exec.CommandContext(r.Context(), ffmpeg, "-nostdin", "-v", "error", "-i", p.AbsolutePath, "-map", "0:v:0", "-map", "0:a:0?", "-vf", "pad=ceil(iw/2)*2:ceil(ih/2)*2", "-c:v", "libx264", "-preset", "veryfast", "-pix_fmt", "yuv420p", "-c:a", "aac", "-movflags", "frag_keyframe+empty_moov+default_base_moof", "-f", "mp4", "pipe:1")
	out, err := cmd.StdoutPipe()
	if err != nil {
		return err
	}
	if err = cmd.Start(); err != nil {
		return bad(503, "Unable to start video conversion")
	}
	// Delay headers until FFmpeg has actually produced a valid stream.
	buf := make([]byte, 32*1024)
	n, readErr := out.Read(buf)
	if n == 0 {
		_ = cmd.Wait()
		if r.Context().Err() != nil {
			return nil
		}
		return bad(422, "Video conversion failed")
	}
	w.Header().Set("Content-Type", "video/mp4")
	w.Header().Set("Cache-Control", "no-store")
	if _, err = w.Write(buf[:n]); err == nil && readErr == nil {
		_, err = io.Copy(w, out)
	}
	if err != nil {
		_ = cmd.Process.Kill()
	}
	if waitErr := cmd.Wait(); waitErr != nil && r.Context().Err() == nil {
		log.Printf("video conversion: %v", waitErr)
	}
	return nil
}
