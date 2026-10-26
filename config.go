package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net"
	"net/url"
	"os"
	"path/filepath"
)

type Config struct {
	JPEGDecoder  string   `json:"jpeg_decoder,omitempty"`
	Root         string   `json:"root"`
	DataDir      string   `json:"data_dir"`
	Host         string   `json:"host"`
	Port         int      `json:"port"`
	ScanInterval int      `json:"scan_interval_seconds"`
	CORSOrigins  []string `json:"cors_origins"`
	FFmpeg       string   `json:"ffmpeg,omitempty"`
	MaxPixels    int64    `json:"max_pixels"`
	ML           MLConfig `json:"ml"`
}

type MLConfig struct {
	URL            string  `json:"url"`
	Enabled        bool    `json:"enabled"`
	ClipModel      string  `json:"clip_model"`
	FaceModel      string  `json:"face_model"`
	IntervalMS     int     `json:"interval_ms"`
	FaceSimilarity float32 `json:"face_similarity"`
}

func (c *MLConfig) defaults() {
	if c.ClipModel == "" {
		c.ClipModel = "ViT-B-32__openai"
	}
	if c.FaceModel == "" {
		c.FaceModel = "buffalo_l"
	}
	if c.IntervalMS < 1000 {
		c.IntervalMS = 2000
	}
	if c.FaceSimilarity == 0 {
		c.FaceSimilarity = 0.6
	}
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
	if c.JPEGDecoder == "" {
		c.JPEGDecoder = "auto"
	}
	if c.JPEGDecoder != "auto" && c.JPEGDecoder != "go" {
		return c, fmt.Errorf("jpeg_decoder must be auto or go")
	}
	c.ML.defaults()
	if c.ML.URL != "" {
		u, e := url.Parse(c.ML.URL)
		if e != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" {
			return c, fmt.Errorf("ml.url must be an HTTP(S) base URL")
		}
	}
	if c.ML.Enabled && c.ML.URL == "" {
		return c, fmt.Errorf("ml.url required when enabled")
	}
	if c.ML.FaceSimilarity < 0.1 || c.ML.FaceSimilarity > 0.99 {
		return c, fmt.Errorf("ml.face_similarity must be between 0.1 and 0.99")
	}
	return c, nil
}

func jpegDecoderName(c Config) string {
	if c.JPEGDecoder == "go" {
		return "go"
	}
	return nativeJPEGName()
}
