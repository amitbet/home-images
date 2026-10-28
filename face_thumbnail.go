package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"image"
	"image/jpeg"
	"log"
	"math"
	"time"

	"github.com/disintegration/imaging"
)

type faceCrop struct {
	Box    faceBox `json:"box"`
	Width  int     `json:"width"`
	Height int     `json:"height"`
}

func cropFace(img image.Image, c *faceCrop) (image.Image, error) {
	b := c.Box
	if c.Width < 1 || c.Height < 1 || math.IsNaN(b.X1+b.X2+b.Y1+b.Y2) || math.IsInf(b.X1+b.X2+b.Y1+b.Y2, 0) || b.X1 < 0 || b.Y1 < 0 || b.X2 > float64(c.Width) || b.Y2 > float64(c.Height) || b.X2 <= b.X1 || b.Y2 <= b.Y1 {
		return nil, bad(422, "Invalid face dimensions")
	}
	sx, sy := float64(img.Bounds().Dx())/float64(c.Width), float64(img.Bounds().Dy())/float64(c.Height)
	padding := int((b.X2 - b.X1) * sx * .22)
	rect := image.Rect(int(b.X1*sx)-padding, int(b.Y1*sy)-padding, int(b.X2*sx)+padding, int(b.Y2*sy)+padding).Intersect(img.Bounds())
	if rect.Empty() {
		return nil, bad(422, "Face is outside the image")
	}
	return imaging.Fill(imaging.Crop(img, rect), 160, 160, imaging.Center, imaging.Linear), nil
}

// Reuse the already decoded ML preview; never reopen the original for new faces.
func (l *Library) seedFaceThumbnails(ctx context.Context, p Photo, preview []byte, pred mlPrediction, signature ...string) {
	if l.thumbnails == nil || len(pred.Faces) == 0 {
		return
	}
	if len(signature) > 0 {
		file, e := l.open(p.Path)
		if e != nil {
			return
		}
		info, e := file.Stat()
		file.Close()
		if e != nil || fmt.Sprintf("go1:%d:%d", info.Size(), info.ModTime().UnixNano()) != signature[0] {
			return
		}
	}
	img, err := jpeg.Decode(bytes.NewReader(preview))
	if err != nil {
		return
	}
	for _, f := range pred.Faces {
		if ctx.Err() != nil {
			return
		}
		if f.Score < .8 {
			continue
		}
		b := f.Box
		b.X1 = max(0, min(float64(pred.Width), b.X1))
		b.X2 = max(0, min(float64(pred.Width), b.X2))
		b.Y1 = max(0, min(float64(pred.Height), b.Y1))
		b.Y2 = max(0, min(float64(pred.Height), b.Y2))
		if b.X2 <= b.X1 || b.Y2 <= b.Y1 {
			continue
		}
		box, _ := json.Marshal(b)
		key, err := l.previewKey(p, fmt.Sprintf("face:%s:%d:%d:160", box, pred.Width, pred.Height))
		if err != nil {
			return
		}
		_, _ = l.thumbnails.get(ctx, key, func() ([]byte, error) {
			thumb, e := cropFace(img, &faceCrop{Box: b, Width: pred.Width, Height: pred.Height})
			if e != nil {
				return nil, e
			}
			var out bytes.Buffer
			e = jpeg.Encode(&out, thumb, &jpeg.Options{Quality: 80})
			return out.Bytes(), e
		})
	}
}

// Warm the same highest-scoring face selected by faceImage, largest groups first.
// Share the single background decoder with indexing and yield to foreground work.
func (l *Library) warmPeopleThumbnails(ctx context.Context) {
	if !waitContext(ctx, 5*time.Second) {
		return
	}
	for ctx.Err() == nil {
		if err := l.warmPeoplePass(ctx); err != nil && ctx.Err() == nil {
			log.Printf("People thumbnail warming: %v", err)
		}
		if !waitContext(ctx, 10*time.Minute) {
			return
		}
	}
}
func (l *Library) warmPeoplePass(ctx context.Context) error {
	if !l.ai.waitIndexing(ctx, true) {
		return ctx.Err()
	}
	rows, err := l.ai.read.QueryContext(ctx, `WITH ranked AS (
 SELECT f.image_id,f.box,f.width,f.height,
 row_number() OVER(PARTITION BY f.person_id ORDER BY f.score DESC,f.id) AS rank,
 count(*) OVER(PARTITION BY f.person_id) AS count
 FROM faces f JOIN ai_images a ON a.image_id=f.image_id
 JOIN catalog.images i ON i.id=a.image_id AND i.signature=a.signature
 WHERE a.models=? AND a.error='' AND i.id NOT IN(SELECT image_id FROM curation.annotations WHERE root=? AND hidden=1))
 SELECT image_id,box,width,height FROM ranked WHERE rank=1 ORDER BY count DESC,image_id`, l.ai.models, l.rootPath)
	if err != nil {
		return err
	}
	type cover struct {
		id, box       string
		width, height int
	}
	var covers []cover
	for rows.Next() {
		var c cover
		if err = rows.Scan(&c.id, &c.box, &c.width, &c.height); err != nil {
			break
		}
		covers = append(covers, c)
	}
	if err == nil {
		err = rows.Err()
	}
	rows.Close()
	if err != nil {
		return err
	}
	for _, c := range covers {
		// Cached covers still open originals to validate signatures. Pace those
		// checks too, and honor pause/browsing before touching the next file.
		if !waitContext(ctx, 20*time.Millisecond) || !l.ai.waitIndexing(ctx, true) {
			return ctx.Err()
		}
		p, e := l.get(ctx, c.id)
		if e != nil {
			continue
		}
		key, e := l.previewKey(p, fmt.Sprintf("face:%s:%d:%d:160", c.box, c.width, c.height))
		if e != nil || l.thumbnails.exists(key) {
			continue
		}
		if !l.ai.waitIndexing(ctx, true) {
			return ctx.Err()
		}
		if _, e = l.faceThumbnail(ctx, p, c.box, c.width, c.height, true); e != nil && ctx.Err() != nil {
			return ctx.Err()
		}
		if !waitContext(ctx, 100*time.Millisecond) {
			return ctx.Err()
		}
	}
	return nil
}
