package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net"
	"os"
	"path/filepath"
)

type Config struct {
	Root         string   `json:"root"`
	DataDir      string   `json:"data_dir"`
	Host         string   `json:"host"`
	Port         int      `json:"port"`
	ScanInterval int      `json:"scan_interval_seconds"`
	CORSOrigins  []string `json:"cors_origins"`
	FFmpeg       string   `json:"ffmpeg,omitempty"`
	MaxPixels    int64    `json:"max_pixels"`
}

func loadConfig(path string) (Config, error) {
	c := Config{Host: "127.0.0.1", Port: 8372, ScanInterval: 900, FFmpeg: "ffmpeg", MaxPixels: 80_000_000}
	b, err := os.ReadFile(path)
	if err != nil {
		return c, err
	}
	if err = json.Unmarshal(bytes.TrimPrefix(b, []byte{0xef, 0xbb, 0xbf}), &c); err != nil {
		return c, err
	}
	if c.Root == "" || c.DataDir == "" || !filepath.IsAbs(c.Root) || !filepath.IsAbs(c.DataDir) {
		return c, fmt.Errorf("root and data_dir must be absolute paths")
	}
	if c.Port < 1 || c.Port > 65535 || c.ScanInterval < 30 || c.MaxPixels < 1 {
		return c, fmt.Errorf("invalid port, scan interval (minimum 30), or max_pixels")
	}
	if c.Host != "" && net.ParseIP(c.Host) == nil && c.Host != "localhost" {
		return c, fmt.Errorf("host must be an IP address or localhost")
	}
	return c, nil
}
