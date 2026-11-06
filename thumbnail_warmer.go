package main

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"strings"
	"time"
)

// A coalesced queue is filled by catalog writes (watcher, imports and scans).
// Startup recovers unfinished work from the catalog; idle workers do no polling.
func (l *Library) thumbnailSignalLocked() chan struct{} {
	if l.thumbnailWake == nil {
		l.thumbnailWake = make(chan struct{}, 1)
	}
	return l.thumbnailWake
}
func (l *Library) queueVideoThumbnail(p Photo) {
	if !isVideo(p.Path) {
		return
	}
	l.thumbnailMu.Lock()
	if l.thumbnailPending == nil {
		l.thumbnailPending = make(map[string]Photo)
	}
	l.thumbnailPending[p.Path] = p
	select {
	case l.thumbnailSignalLocked() <- struct{}{}:
	default:
	}
	l.thumbnailMu.Unlock()
}
func (l *Library) takeThumbnailJobs() []Photo {
	l.thumbnailMu.Lock()
	defer l.thumbnailMu.Unlock()
	jobs := make([]Photo, 0, len(l.thumbnailPending))
	for _, p := range l.thumbnailPending {
		jobs = append(jobs, p)
	}
	l.thumbnailPending = nil
	return jobs
}
func (l *Library) runThumbnailWarming(ctx context.Context) {
	if !waitContext(ctx, 5*time.Second) {
		return
	}
	l.thumbnailMu.Lock()
	wake := l.thumbnailSignalLocked()
	l.thumbnailMu.Unlock()
	if err := l.warmThumbnails(ctx); err != nil && ctx.Err() == nil {
		log.Printf("Video thumbnail startup pass: %v", err)
	}
	log.Print("Video thumbnail startup pass finished; waiting for new or changed videos")
	for {
		select {
		case <-ctx.Done():
			return
		case <-wake:
			for _, p := range l.takeThumbnailJobs() {
				if err := l.warmVideoThumbnail(ctx, p); err != nil && ctx.Err() == nil {
					log.Printf("Video thumbnail %s: %v", p.ID, err)
				}
			}
		}
	}
}

// Visit only videos, once. Cached videos are checked but never regenerated.
func (l *Library) warmThumbnails(ctx context.Context) error {
	cursor := ""
	for ctx.Err() == nil {
		rows, err := l.db.QueryContext(ctx, "SELECT path,metadata_json(metadata) FROM images WHERE path>? COLLATE BINARY AND NOT ("+strings.ReplaceAll(photoOnlySQL, "i.path", "path")+") ORDER BY path COLLATE BINARY LIMIT 24", cursor)
		if err != nil {
			return err
		}
		var photos []Photo
		for rows.Next() {
			var path, raw string
			if err = rows.Scan(&path, &raw); err != nil {
				break
			}
			var p Photo
			if err = json.Unmarshal([]byte(raw), &p); err != nil {
				break
			}
			photos = append(photos, p)
		}
		rowErr := rows.Err()
		rows.Close()
		if err != nil {
			return err
		}
		if rowErr != nil {
			return rowErr
		}
		if len(photos) == 0 {
			return nil
		}
		for _, p := range photos {
			cursor = p.Path
			if err := l.warmVideoThumbnail(ctx, p); err != nil && ctx.Err() == nil {
				log.Printf("Video thumbnail %s: %v", p.ID, err)
			}
		}
	}
	return ctx.Err()
}

// Notifications keep signatures current, so cached startup work need not open
// originals merely to rediscover that their previews already exist.
func (l *Library) warmingPreviewKey(ctx context.Context, p Photo, variant string) (string, error) {
	var signature string
	if err := l.db.QueryRowContext(ctx, "SELECT signature FROM images WHERE id=? AND path=?", p.ID, p.Path).Scan(&signature); err != nil {
		return "", err
	}
	var size, modified int64
	if n, err := fmt.Sscanf(signature, "go1:%d:%d", &size, &modified); err == nil && n == 2 {
		return l.previewKeyForFile(p, variant, size, modified), nil
	}
	return l.previewKey(p, variant)
}

func (l *Library) warmVideoThumbnail(ctx context.Context, p Photo) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	key, err := l.warmingPreviewKey(ctx, p, fmt.Sprintf("image:%d:%d:%d", thumbnailWidth, thumbnailHeight, thumbnailQuality))
	if err != nil {
		return err
	}
	if l.thumbnails.exists(key) {
		return nil
	}
	for l.ai.paused.Load() || l.renderSlots.active() > 0 || time.Since(time.Unix(0, l.lastRender.Load())) < 2*time.Second {
		if !waitContext(ctx, time.Second) {
			return ctx.Err()
		}
	}
	_, err = l.thumbnails.get(ctx, key, func(ctx context.Context) ([]byte, error) {
		select {
		case l.previewDecoder <- struct{}{}:
			defer func() { <-l.previewDecoder }()
		case <-ctx.Done():
			return nil, ctx.Err()
		}
		if l.isolateRendering {
			return l.processPreview(ctx, p, thumbnailWidth, thumbnailHeight, thumbnailQuality)
		}
		return l.renderJPEG(ctx, p, thumbnailWidth, thumbnailHeight, thumbnailQuality)
	})
	if err == nil && !waitContext(ctx, 2*time.Second) {
		return ctx.Err()
	}
	return err
}
