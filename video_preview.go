package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"image/jpeg"
	"log"
	"os/exec"
	"path/filepath"
	"strconv"
	"time"
)

// Called within the shared render queue (at most two foreground decoders).
// Extract only on demand; originals are never modified.
func (l *Library) videoPreview(ctx context.Context, path string, width, height, quality int) ([]byte, error) {
	started := time.Now()
	work, cancel := context.WithTimeout(ctx, 8*time.Second)
	defer cancel()
	ffmpeg, err := exec.LookPath(l.config.FFmpeg)
	if err == nil {
		probe := filepath.Join(filepath.Dir(ffmpeg), "ffprobe"+filepath.Ext(ffmpeg))
		cmd := exec.CommandContext(work, probe, "-v", "error", "-show_entries", "stream=index:stream_disposition=attached_pic", "-of", "json", path)
		configurePreview(cmd)
		var metadata limitedBuffer
		metadata.max = 1 << 20
		cmd.Stdout = &metadata
		cover := -1
		if cmd.Run() == nil {
			var info struct {
				Streams []struct {
					Index       int `json:"index"`
					Disposition struct {
						Attached int `json:"attached_pic"`
					} `json:"disposition"`
				} `json:"streams"`
			}
			if json.Unmarshal(metadata.Bytes(), &info) == nil {
				for _, s := range info.Streams {
					if s.Disposition.Attached != 0 {
						cover = s.Index
						break
					}
				}
			}
		}
		maps := []string{"0:V:0"}
		if cover >= 0 {
			maps = append([]string{"0:" + strconv.Itoa(cover)}, maps...)
		}
		for i, stream := range maps {
			args := []string{"-nostdin", "-v", "error", "-threads", "1", "-filter_threads", "1", "-i", path, "-map", stream, "-frames:v", "1", "-an", "-sn", "-vf", fmt.Sprintf("scale=%d:%d:force_original_aspect_ratio=decrease", width, height), "-map_metadata", "-1", "-f", "image2pipe", "-c:v", "mjpeg", "-threads", "1", "-q:v", fmt.Sprint(2 + (95-quality)*29/55), "pipe:1"}
			cmd := exec.CommandContext(work, ffmpeg, args...)
			configurePreview(cmd)
			var out limitedBuffer
			out.max = 32 << 20
			cmd.Stdout = &out
			err = cmd.Run()
			if err == nil {
				_, err = jpeg.DecodeConfig(bytes.NewReader(out.Bytes()))
				if err == nil {
					source := "first-frame"
					if cover >= 0 && i == 0 {
						source = "embedded-cover"
					}
					log.Printf("Video preview: source=%s elapsed=%s", source, time.Since(started).Round(time.Millisecond))
					return out.Bytes(), nil
				}
			}
			if work.Err() != nil {
				break
			}
		}
	}
	if ctx.Err() != nil {
		return nil, ctx.Err()
	}
	log.Printf("Video preview: source=placeholder elapsed=%s error=%v", time.Since(started).Round(time.Millisecond), err)
	return videoThumbnail(width, height, quality)
}
