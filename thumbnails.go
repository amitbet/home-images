package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"image/jpeg"
	"io/fs"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/disintegration/imaging"
)

const thumbnailWidth, thumbnailHeight, thumbnailQuality = 480, 360, 72
const thumbnailBudget = int64(4 << 30)

type thumbnailFlight struct {
	done chan struct{}
	data []byte
	err  error
}
type thumbnailCache struct {
	root      string
	mu        sync.Mutex
	flights   map[string]*thumbnailFlight
	nextPrune time.Time
	pruneMu   sync.Mutex
}

func newThumbnailCache(dataDir string) *thumbnailCache {
	return &thumbnailCache{root: filepath.Join(dataDir, "thumbnails-v1"), flights: map[string]*thumbnailFlight{}}
}
func (l *Library) previewKey(p Photo, variant string) (string, error) {
	f, err := l.open(p.Path)
	if err != nil {
		return "", err
	}
	info, err := f.Stat()
	f.Close()
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(fmt.Appendf(nil, "v1|%s|%s|%d|%d|%s", l.rootPath, p.Path, info.ModTime().UnixNano(), info.Size(), variant))
	return hex.EncodeToString(sum[:]), nil
}
func (c *thumbnailCache) path(key string) string { return filepath.Join(c.root, key[:2], key+".jpg") }
func (c *thumbnailCache) exists(key string) bool {
	info, err := os.Stat(c.path(key))
	return err == nil && info.Size() > 4 && info.Size() <= 32<<20
}
func (c *thumbnailCache) read(key string) ([]byte, bool) {
	path := c.path(key)
	info, err := os.Stat(path)
	if err != nil || info.Size() > 32<<20 {
		return nil, false
	}
	data, err := os.ReadFile(path)
	if err != nil || len(data) < 4 || data[0] != 0xff || data[1] != 0xd8 || data[len(data)-2] != 0xff || data[len(data)-1] != 0xd9 {
		return nil, false
	}
	if time.Since(info.ModTime()) > 5*time.Minute {
		now := time.Now()
		_ = os.Chtimes(path, now, now)
	}
	return data, true
}
func (c *thumbnailCache) store(key string, data []byte) error {
	path := c.path(key)
	if err := os.MkdirAll(filepath.Dir(path), 0750); err != nil {
		return err
	}
	f, err := os.CreateTemp(filepath.Dir(path), ".thumbnail-*")
	if err != nil {
		return err
	}
	tmp := f.Name()
	defer os.Remove(tmp)
	if _, err = f.Write(data); err != nil {
		f.Close()
		return err
	}
	if err = f.Close(); err != nil {
		return err
	}
	if err = os.Rename(tmp, path); err != nil {
		if _, ok := c.read(key); ok {
			return nil
		}
		return err
	}
	// Enforce the shared disk budget even during continuous foreground browsing.
	c.mu.Lock()
	if time.Now().After(c.nextPrune) {
		c.nextPrune = time.Now().Add(time.Minute)
		go c.prune(thumbnailBudget)
	}
	c.mu.Unlock()
	return nil
}
func (c *thumbnailCache) get(ctx context.Context, key string, build func() ([]byte, error)) ([]byte, error) {
	for {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		if data, ok := c.read(key); ok {
			return data, nil
		}
		c.mu.Lock()
		if call := c.flights[key]; call != nil {
			c.mu.Unlock()
			select {
			case <-ctx.Done():
				return nil, ctx.Err()
			case <-call.done:
				if errors.Is(call.err, context.Canceled) || errors.Is(call.err, context.DeadlineExceeded) {
					continue
				}
				return call.data, call.err
			}
		}
		call := &thumbnailFlight{done: make(chan struct{})}
		c.flights[key] = call
		c.mu.Unlock()
		// Recheck after claiming the key; another request may have just stored it.
		if data, ok := c.read(key); ok {
			call.data = data
		} else {
			call.data, call.err = build()
			if call.err == nil && ctx.Err() == nil {
				_ = c.store(key, call.data)
			}
		}
		c.mu.Lock()
		delete(c.flights, key)
		close(call.done)
		c.mu.Unlock()
		return call.data, call.err
	}
}

// Cache thumbnails and the standard viewer variant. Arbitrary large dimensions
// remain uncached to keep the shared disk budget useful.
func (l *Library) jpeg(ctx context.Context, p Photo, width, height, quality int) ([]byte, error) {
	build := func() ([]byte, error) {
		l.lastRender.Store(time.Now().UnixNano())
		defer func() { l.lastRender.Store(time.Now().UnixNano()) }()
		if l.renderSlots != nil {
			select {
			case l.renderSlots <- struct{}{}:
				defer func() { <-l.renderSlots }()
			case <-ctx.Done():
				return nil, ctx.Err()
			}
		}
		if l.isolateRendering {
			return l.processPreview(ctx, p, width, height, quality)
		}
		return l.renderJPEG(ctx, p, width, height, quality)
	}
	if l.thumbnails == nil || ((width > 640 || height > 640) && !((width == 1920 && height == 1440 && quality == 85) || (width == 1280 && height == 1280 && quality == 80))) {
		return build()
	}
	key, err := l.previewKey(p, fmt.Sprintf("image:%d:%d:%d", width, height, quality))
	if err != nil {
		return nil, err
	}
	return l.thumbnails.get(ctx, key, build)
}
func (l *Library) thumbnail(w http.ResponseWriter, r *http.Request) error {
	clone := r.Clone(r.Context())
	u := *r.URL
	clone.URL = &u
	q := u.Query()
	q.Set("width", fmt.Sprint(thumbnailWidth))
	q.Set("height", fmt.Sprint(thumbnailHeight))
	q.Set("quality", fmt.Sprint(thumbnailQuality))
	u.RawQuery = q.Encode()
	return l.render(w, clone)
}

func (l *Library) faceThumbnail(ctx context.Context, p Photo, box string, width, height int, background ...bool) ([]byte, error) {
	key, err := l.previewKey(p, fmt.Sprintf("face:%s:%d:%d:160", box, width, height))
	if err != nil {
		return nil, err
	}
	var b faceBox
	if err = json.Unmarshal([]byte(box), &b); err != nil {
		return nil, err
	}
	crop := &faceCrop{Box: b, Width: width, Height: height}
	build := func() ([]byte, error) {
		if l.isolateRendering {
			if len(background) > 0 && background[0] {
				select {
				case l.previewDecoder <- struct{}{}:
					defer func() { <-l.previewDecoder }()
				case <-ctx.Done():
					return nil, ctx.Err()
				}
				return l.processPreview(ctx, p, 1280, 1280, 80, crop)
			}
			l.lastRender.Store(time.Now().UnixNano())
			defer func() { l.lastRender.Store(time.Now().UnixNano()) }()
			select {
			case l.renderSlots <- struct{}{}:
				defer func() { <-l.renderSlots }()
			case <-ctx.Done():
				return nil, ctx.Err()
			}
			return l.processPreview(ctx, p, 1280, 1280, 80, crop)
		}
		worker := &Library{config: l.config, root: l.root, rootPath: l.rootPath, faceCrop: crop}
		return worker.renderJPEG(ctx, p, 1280, 1280, 80)
	}
	if l.thumbnails == nil {
		return build()
	}
	return l.thumbnails.get(ctx, key, build)
}

// Seed a gallery thumbnail from the already-oriented ML preview, avoiding a
// second decode of the original. Inference and gallery thumbnails share a decode.
func (l *Library) seedThumbnail(p Photo, preview []byte, key string) {
	if l.thumbnails == nil {
		return
	}
	current, err := l.previewKey(p, fmt.Sprintf("image:%d:%d:%d", thumbnailWidth, thumbnailHeight, thumbnailQuality))
	if err != nil || current != key {
		return
	}
	if _, ok := l.thumbnails.read(key); ok {
		return
	}
	_, _ = l.thumbnails.get(context.Background(), key, func() ([]byte, error) {
		img, err := jpeg.Decode(bytes.NewReader(preview))
		if err != nil {
			return nil, err
		}
		img = imaging.Fit(img, thumbnailWidth, thumbnailHeight, imaging.Lanczos)
		var out bytes.Buffer
		err = jpeg.Encode(&out, img, &jpeg.Options{Quality: thumbnailQuality})
		return out.Bytes(), err
	})
}

func (l *Library) warmThumbnails(ctx context.Context) {
	if !waitContext(ctx, 5*time.Second) {
		return
	}
	l.thumbnails.prune(thumbnailBudget)
	cursor := ""
	completed := 0
	for ctx.Err() == nil {
		rows, err := l.db.QueryContext(ctx, "SELECT path,metadata_json(metadata) FROM images WHERE path>? COLLATE BINARY ORDER BY path COLLATE BINARY LIMIT 24", cursor)
		if err != nil {
			if !waitContext(ctx, 30*time.Second) {
				return
			}
			continue
		}
		var photos []Photo
		for rows.Next() {
			var path, raw string
			if err = rows.Scan(&path, &raw); err != nil {
				break
			}
			var p Photo
			if json.Unmarshal([]byte(raw), &p) == nil {
				photos = append(photos, p)
			}
		}
		rows.Close()
		if len(photos) == 0 {
			cursor = ""
			if !waitContext(ctx, 10*time.Minute) {
				return
			}
			continue
		}
		for _, p := range photos {
			cursor = p.Path
			if ctx.Err() != nil {
				return
			}
			key, err := l.previewKey(p, fmt.Sprintf("image:%d:%d:%d", thumbnailWidth, thumbnailHeight, thumbnailQuality))
			if err != nil {
				continue
			}
			if l.thumbnails.exists(key) {
				continue
			}
			// Foreground browsing always takes priority over speculative warming.
			for l.ai.paused.Load() || len(l.renderSlots) > 0 || time.Since(time.Unix(0, l.lastRender.Load())) < 2*time.Second {
				if !waitContext(ctx, time.Second) {
					return
				}
			}
			if _, err = l.aiPreview(ctx, p); err != nil && ctx.Err() == nil {
				log.Printf("Thumbnail %s: %v", p.ID, err)
			}
			completed++
			if completed%200 == 0 {
				l.thumbnails.prune(thumbnailBudget)
			}
			if !waitContext(ctx, 2*time.Second) {
				return
			}
		}
	}
}

// The cache is disposable. Only generated hash-named JPEGs inside its own
// directory are eligible for eviction; originals and annotations are untouched.
func (c *thumbnailCache) prune(limit int64) {
	c.pruneMu.Lock()
	defer c.pruneMu.Unlock()
	type entry struct {
		path     string
		size     int64
		modified time.Time
	}
	var files []entry
	var total int64
	_ = filepath.WalkDir(c.root, func(path string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return nil
		}
		name := d.Name()
		if len(name) != 68 || !strings.HasSuffix(name, ".jpg") {
			return nil
		}
		if _, err := hex.DecodeString(name[:64]); err != nil {
			return nil
		}
		info, err := d.Info()
		if err == nil {
			files = append(files, entry{path, info.Size(), info.ModTime()})
			total += info.Size()
		}
		return nil
	})
	if total <= limit {
		return
	}
	sort.Slice(files, func(i, j int) bool { return files[i].modified.Before(files[j].modified) })
	for _, f := range files {
		if total <= limit*9/10 {
			break
		}
		if os.Remove(f.path) == nil {
			total -= f.size
		}
	}
}
