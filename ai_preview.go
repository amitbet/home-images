package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

// Decode large originals outside the HTTP process. This worker has one Go CPU,
// a soft memory budget, a timeout, and below-normal priority on Windows. It never
// opens either SQLite index and cannot consume the foreground render slots.
func (l *Library) aiPreview(ctx context.Context, p Photo) ([]byte, error) {
	if l.previewDecoder != nil {
		select {
		case l.previewDecoder <- struct{}{}:
			defer func() { <-l.previewDecoder }()
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
	key, _ := l.previewKey(p, fmt.Sprintf("image:%d:%d:%d", thumbnailWidth, thumbnailHeight, thumbnailQuality))
	data, err := l.processPreview(ctx, p, 1280, 1280, 80)
	if err == nil && key != "" {
		l.seedThumbnail(p, data, key)
	}
	return data, err
}

type previewRequest struct {
	Config
	Width   int `json:"preview_width"`
	Height  int `json:"preview_height"`
	Quality int `json:"preview_quality"`
}

func (l *Library) processPreview(ctx context.Context, p Photo, width, height, quality int) ([]byte, error) {
	ctx, cancel := context.WithTimeout(ctx, 100*time.Second)
	defer cancel()
	data, err := l.processPreviewConfig(ctx, p, width, height, quality, l.config)
	if err != nil && ctx.Err() == nil && l.config.JPEGDecoder != "go" && nativeJPEGName() == "turbojpeg" && (p.Format == "JPEG" || strings.EqualFold(filepath.Ext(p.Path), ".jpg") || strings.EqualFold(filepath.Ext(p.Path), ".jpeg")) {
		fallback := l.config
		fallback.JPEGDecoder = "go"
		return l.processPreviewConfig(ctx, p, width, height, quality, fallback)
	}
	return data, err
}
func (l *Library) processPreviewConfig(ctx context.Context, p Photo, width, height, quality int, config Config) ([]byte, error) {
	exe, err := os.Executable()
	if err != nil {
		return nil, err
	}
	data, err := json.Marshal(previewRequest{Config: config, Width: width, Height: height, Quality: quality})
	if err != nil {
		return nil, err
	}
	ctx, cancel := context.WithTimeout(ctx, 100*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, exe, "--ai-preview", p.Path)
	cmd.Env = append(os.Environ(), "GOMAXPROCS=1", "GOMEMLIMIT=256MiB")
	configurePreview(cmd)
	cmd.Stdin = bytes.NewReader(data)
	var stdout, stderr limitedBuffer
	stdout.max = 32 << 20
	stderr.max = 4096
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	if err = cmd.Run(); err != nil {
		return nil, fmt.Errorf("background preview: %w: %s", err, stderr.String())
	}
	return stdout.Bytes(), nil
}
func runAIPreview(path string) error {
	_ = os.Setenv("HOME_IMAGES_AI_PREVIEW", "1")
	var request previewRequest
	if err := json.NewDecoder(io.LimitReader(os.Stdin, 64<<10)).Decode(&request); err != nil {
		return err
	}
	c := request.Config
	if request.Width == 0 {
		request.Width = 1280
	}
	if request.Height == 0 {
		request.Height = 1280
	}
	if request.Quality == 0 {
		request.Quality = 80
	}
	if request.Width < 32 || request.Width > 2560 || request.Height < 32 || request.Height > 2560 || request.Quality < 40 || request.Quality > 95 {
		return fmt.Errorf("invalid preview dimensions or quality")
	}
	if !filepath.IsAbs(c.Root) || c.MaxPixels < 1 {
		return fmt.Errorf("invalid preview configuration")
	}
	rootPath, err := filepath.EvalSymlinks(c.Root)
	if err != nil {
		return err
	}
	root, err := os.OpenRoot(rootPath)
	if err != nil {
		return err
	}
	defer root.Close()
	l := &Library{config: c, rootPath: rootPath, root: root}
	ctx, cancel := context.WithTimeout(context.Background(), 95*time.Second)
	defer cancel()
	data, err := l.jpeg(ctx, Photo{Path: path}, request.Width, request.Height, request.Quality)
	if err != nil {
		return err
	}
	_, err = os.Stdout.Write(data)
	return err
}
