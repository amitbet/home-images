package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"image"
	"image/color"
	"image/jpeg"
	"net/http"
	"testing"
)

func TestFaceCropUsesFaceRegion(t *testing.T) {
	img := image.NewRGBA(image.Rect(0, 0, 200, 100))
	for y := 0; y < 100; y++ {
		for x := 0; x < 200; x++ {
			c := color.RGBA{R: 255, A: 255}
			if x >= 100 {
				c = color.RGBA{B: 255, A: 255}
			}
			img.Set(x, y, c)
		}
	}
	crop := &faceCrop{Box: faceBox{X1: 120, Y1: 20, X2: 180, Y2: 80}, Width: 200, Height: 100}
	out, err := cropFace(img, crop)
	if err != nil {
		t.Fatal(err)
	}
	if out.Bounds().Dx() != 160 || out.Bounds().Dy() != 160 {
		t.Fatal(out.Bounds())
	}
	r, _, b, _ := out.At(80, 80).RGBA()
	if b < 60000 || r > 1000 {
		t.Fatal("cropped wrong region")
	}
	crop.Width = 0
	if _, err = cropFace(img, crop); err == nil {
		t.Fatal("accepted invalid dimensions")
	}
}
func TestFaceSeedAndOnDemandThumbnail(t *testing.T) {
	l, s, _ := fixture(t)
	ctx := context.Background()
	j := testJob(t, l, "a.jpg")
	p, err := l.get(ctx, j.ID)
	if err != nil {
		t.Fatal(err)
	}
	preview, err := l.renderJPEG(ctx, p, 1280, 1280, 80)
	if err != nil {
		t.Fatal(err)
	}
	pred := syntheticPrediction()
	l.seedFaceThumbnails(ctx, p, preview, pred)
	box, _ := json.Marshal(pred.Faces[0].Box)
	key, err := l.previewKey(p, fmt.Sprintf("face:%s:%d:%d:160", box, pred.Width, pred.Height))
	if err != nil {
		t.Fatal(err)
	}
	data, ok := l.thumbnails.read(key)
	if !ok {
		t.Fatal("indexing did not seed face cache")
	}
	cfg, err := jpeg.DecodeConfig(bytes.NewReader(data))
	if err != nil || cfg.Width != 160 || cfg.Height != 160 {
		t.Fatal(cfg, err)
	}
	if err = l.ai.save(ctx, j, pred); err != nil {
		t.Fatal(err)
	}
	var personID int64
	if err = l.ai.read.QueryRowContext(ctx, "SELECT person_id FROM faces WHERE image_id=?", j.ID).Scan(&personID); err != nil {
		t.Fatal(err)
	}
	l.thumbnails = newThumbnailCache(t.TempDir())
	res, err := s.Client().Get(fmt.Sprintf("%s/api/people/%d/image", s.URL, personID))
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusOK || res.Header.Get("Content-Type") != "image/jpeg" {
		t.Fatalf("unexpected face thumbnail response: %s %s", res.Status, res.Header.Get("Content-Type"))
	}
	if _, ok = l.thumbnails.read(key); !ok {
		t.Fatal("requested people cover was not cached")
	}
}
