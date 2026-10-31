package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"image"
	"image/color"
	"image/draw"
	_ "image/gif"
	"image/jpeg"
	_ "image/png"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/disintegration/imaging"
	"github.com/djherbis/times"
	"github.com/evanoberholster/imagemeta"
	rawexif "github.com/rwcarlsen/goexif/exif"
	"github.com/rwcarlsen/goexif/tiff"
	_ "golang.org/x/image/bmp"
	_ "golang.org/x/image/tiff"
	_ "golang.org/x/image/webp"
)

func asMap(value any) map[string]any {
	b, err := json.Marshal(value)
	if err != nil {
		return map[string]any{}
	}
	m := map[string]any{}
	_ = json.Unmarshal(b, &m)
	return m
}

func (l *Library) metadata(ctx context.Context, relative string) (p Photo, err error) {
	// Malformed camera metadata must not crash an unattended service.
	defer func() {
		if r := recover(); r != nil {
			err = fmt.Errorf("metadata decoder panic: %v", r)
		}
	}()
	f, err := l.open(relative)
	if err != nil {
		return p, err
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil {
		return p, err
	}
	if !info.Mode().IsRegular() {
		return p, fmt.Errorf("not a regular file")
	}
	p = Photo{ID: imageID(relative), Path: relative, AbsolutePath: filepath.Join(l.rootPath, filepath.FromSlash(relative)), Filename: filepath.Base(relative),
		Size: info.Size(), ModifiedAt: info.ModTime().UTC().Format(time.RFC3339Nano), Exif: map[string]any{}}
	p.Directory = filepath.ToSlash(filepath.Dir(relative))
	if p.Directory == "." {
		p.Directory = ""
	}
	p.ImageURL = "/api/images/" + p.ID + "/image"
	p.OriginalURL = "/api/images/" + p.ID + "/original"
	if ts, e := times.StatFile(f); e == nil && ts.HasBirthTime() {
		created := ts.BirthTime().UTC().Format(time.RFC3339Nano)
		p.CreatedAt = &created
	}
	if isVideo(relative) {
		p.Format = "VIDEO"
		return p, nil
	}
	// Metadata is optional. Missing/broken EXIF does not exclude an otherwise decodable photo.
	ex, _ := imagemeta.Decode(f)
	var raw *rawexif.Exif
	if isHEIC(relative) {
		if payload, e := heifExif(f); e == nil {
			// HEIF stores its EXIF in a separate item, rather than a JPEG APP1 segment.
			tiffData := bytes.TrimPrefix(payload, []byte("Exif\x00\x00"))
			if parsed, e := imagemeta.Decode(bytes.NewReader(tiffData)); e == nil {
				ex = parsed
			}
			raw, _ = rawexif.Decode(bytes.NewReader(payload))
		}
	}
	p.Exif = asMap(ex.IFD0)
	p.Exif["Photo"] = asMap(ex.ExifIFD)
	p.Exif["GPS"] = asMap(ex.GPS)
	p.Orientation = int(ex.IFD0.Orientation)
	p.Exif["Orientation"] = p.Orientation
	// Preserve original tag spellings/strings rather than camera-make normalization.
	// imagemeta additionally supplies HEIF metadata and normalized GPS coordinates.
	if raw == nil {
		if _, e := f.Seek(0, io.SeekStart); e == nil {
			raw, _ = rawexif.Decode(f)
		}
	}
	if raw != nil {
		_ = raw.Walk(rawTags(p.Exif))
	}
	if !ex.ExifIFD.DateTimeOriginal.IsZero() {
		taken := ex.ExifIFD.DateTimeOriginal.Format("2006:01:02 15:04:05")
		p.TakenAt = &taken
		if ex.ExifIFD.OffsetTimeOriginal != nil {
			offset := ex.ExifIFD.DateTimeOriginal.In(ex.ExifIFD.OffsetTimeOriginal).Format("-07:00")
			p.TakenAtOffset = &offset
		}
	}
	if _, err = f.Seek(0, io.SeekStart); err != nil {
		return p, err
	}
	c, format, decodeErr := image.DecodeConfig(f)
	heifContainerDimensions := false
	if decodeErr == nil {
		p.Width, p.Height, p.Format = c.Width, c.Height, strings.ToUpper(format)
	} else if isHEIC(relative) {
		p.Format = "HEIF"
		if w, h, e := heifDimensions(f); e == nil {
			p.Width, p.Height = w, h
			heifContainerDimensions = true
		} else {
			p.Width, p.Height = int(ex.IFD0.ImageWidth), int(ex.IFD0.ImageHeight)
		}
		if p.Width == 0 || p.Height == 0 {
			p.Width, p.Height = int(ex.ExifIFD.PixelXDimension), int(ex.ExifIFD.PixelYDimension)
		}
		if p.Width == 0 || p.Height == 0 {
			p.Width, p.Height, err = l.probeHEIC(ctx, p.AbsolutePath)
			if err != nil {
				return p, err
			}
		}
	} else {
		return p, decodeErr
	}
	if p.Width <= 0 || p.Height <= 0 || int64(p.Width) > l.config.MaxPixels/int64(p.Height) {
		return p, fmt.Errorf("image exceeds max_pixels or has invalid dimensions")
	}
	if !heifContainerDimensions && p.Orientation >= 5 && p.Orientation <= 8 {
		p.Width, p.Height = p.Height, p.Width
	}
	return p, nil
}

type rawTags map[string]any

func (m rawTags) Walk(name rawexif.FieldName, tag *tiff.Tag) error {
	b, err := tag.MarshalJSON()
	if err != nil || len(b) > 4096 {
		return nil
	}
	var value any
	if json.Unmarshal(b, &value) == nil {
		m[string(name)] = value
	}
	return nil
}

// No shell is involved. The original path is passed as one argument.
func (l *Library) probeHEIC(ctx context.Context, path string) (int, int, error) {
	ffmpeg, err := exec.LookPath(l.config.FFmpeg)
	if err != nil {
		return 0, 0, fmt.Errorf("HEIC dimensions unavailable and FFmpeg not installed")
	}
	probe := filepath.Join(filepath.Dir(ffmpeg), "ffprobe"+filepath.Ext(ffmpeg))
	ctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, probe, "-v", "error", "-select_streams", "v:0", "-show_entries", "stream=width,height", "-of", "json", path)
	var out limitedBuffer
	out.max = 1 << 20
	cmd.Stdout = &out
	if err = cmd.Run(); err != nil {
		return 0, 0, fmt.Errorf("ffprobe: %w", err)
	}
	var data struct {
		Streams []struct {
			Width  int `json:"width"`
			Height int `json:"height"`
		} `json:"streams"`
	}
	if err = json.Unmarshal(out.Bytes(), &data); err != nil || len(data.Streams) == 0 {
		return 0, 0, fmt.Errorf("HEIC dimensions missing")
	}
	return data.Streams[0].Width, data.Streams[0].Height, nil
}

type limitedBuffer struct {
	bytes.Buffer
	max int
}

func (b *limitedBuffer) Write(p []byte) (int, error) {
	if b.Len()+len(p) > b.max {
		return 0, fmt.Errorf("decoder output limit exceeded")
	}
	return b.Buffer.Write(p)
}

func (l *Library) renderJPEG(ctx context.Context, p Photo, width, height, quality int) (output []byte, err error) {
	defer func() {
		if r := recover(); r != nil {
			err = fmt.Errorf("image decoder panic: %v", r)
		}
	}()
	f, err := l.open(p.Path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	// Re-read dimensions and metadata after a file changes, even before the next scan.
	fresh, err := l.metadata(ctx, p.Path)
	if err != nil {
		return nil, bad(422, "Image cannot be decoded")
	}
	if isVideo(p.Path) {
		if ffmpeg, e := exec.LookPath(l.config.FFmpeg); e == nil {
			frameCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
			defer cancel()
			scale := fmt.Sprintf("scale=w='min(%d,iw)':h='min(%d,ih)':force_original_aspect_ratio=decrease", width, height)
			cmd := exec.CommandContext(frameCtx, ffmpeg, "-nostdin", "-v", "error", "-threads", "1", "-filter_threads", "1", "-i", fresh.AbsolutePath, "-map", "0:v:0", "-frames:v", "1", "-vf", scale, "-map_metadata", "-1", "-f", "image2pipe", "-c:v", "mjpeg", "-q:v", fmt.Sprint(2+(95-quality)*29/55), "pipe:1")
			var out limitedBuffer
			out.max = 32 << 20
			cmd.Stdout = &out
			if cmd.Run() == nil && out.Len() > 0 {
				return out.Bytes(), nil
			}
		}
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		return videoThumbnail(width, height, quality)
	}
	if isHEIC(p.Path) {
		ffmpeg, e := exec.LookPath(l.config.FFmpeg)
		if e != nil {
			return nil, bad(503, "HEIC display requires FFmpeg; set ffmpeg in config")
		}
		// FFmpeg handles HEIF container rotation and EXIF itself; do not rotate twice.
		ctx, cancel := context.WithTimeout(ctx, 90*time.Second)
		defer cancel()
		scale := fmt.Sprintf("scale=w='min(%d,iw)':h='min(%d,ih)':force_original_aspect_ratio=decrease", width, height)
		args := []string{"-nostdin", "-v", "error"}
		if os.Getenv("HOME_IMAGES_AI_PREVIEW") == "1" {
			args = append(args, "-threads", "1", "-filter_threads", "1")
		}
		args = append(args, "-i", fresh.AbsolutePath, "-frames:v", "1", "-vf", scale, "-map_metadata", "-1", "-f", "image2pipe", "-c:v", "mjpeg", "-q:v", fmt.Sprint(2+(95-quality)*29/55))
		if os.Getenv("HOME_IMAGES_AI_PREVIEW") == "1" {
			args = append(args, "-threads", "1")
		}
		args = append(args, "pipe:1")
		cmd := exec.CommandContext(ctx, ffmpeg, args...)
		var out limitedBuffer
		out.max = 32 << 20
		cmd.Stdout = &out
		if e = cmd.Run(); e != nil {
			return nil, bad(422, "FFmpeg could not decode this HEIC image")
		}
		if l.faceCrop != nil {
			img, e := jpeg.Decode(bytes.NewReader(out.Bytes()))
			if e != nil {
				return nil, e
			}
			img, e = cropFace(img, l.faceCrop)
			if e != nil {
				return nil, e
			}
			out.Reset()
			if e = jpeg.Encode(&out, img, &jpeg.Options{Quality: quality}); e != nil {
				return nil, e
			}
		}
		return out.Bytes(), nil
	}
	// Keep all native codec work in the killable child, never in the HTTP process.
	if fresh.Format == "JPEG" && l.config.JPEGDecoder != "go" && os.Getenv("HOME_IMAGES_AI_PREVIEW") == "1" {
		if data, e := nativeJPEGPreview(ctx, f, fresh, width, height, quality, l.config.MaxPixels, l.faceCrop); e == nil {
			return data, nil
		}
		if err = ctx.Err(); err != nil {
			return nil, err
		}
		if _, err = f.Seek(0, 0); err != nil {
			return nil, err
		}
	}
	decoded, _, err := image.Decode(f)
	if err != nil {
		return nil, bad(422, "Image cannot be decoded")
	}
	decoded = orient(decoded, fresh.Orientation)
	if l.faceCrop != nil {
		decoded, err = cropFace(decoded, l.faceCrop)
		if err != nil {
			return nil, err
		}
	} else {
		decoded = imaging.Fit(decoded, width, height, imaging.Lanczos)
	}
	// JPEG has no alpha channel. Composite transparency over white.
	opaque := image.NewRGBA(decoded.Bounds())
	draw.Draw(opaque, opaque.Bounds(), image.NewUniform(color.White), image.Point{}, draw.Src)
	draw.Draw(opaque, opaque.Bounds(), decoded, decoded.Bounds().Min, draw.Over)
	var out bytes.Buffer
	err = jpeg.Encode(&out, opaque, &jpeg.Options{Quality: quality})
	return out.Bytes(), err
}

func orient(img image.Image, orientation int) image.Image {
	switch orientation {
	case 2:
		return imaging.FlipH(img)
	case 3:
		return imaging.Rotate180(img)
	case 4:
		return imaging.FlipV(img)
	case 5:
		return imaging.Transpose(img)
	case 6:
		return imaging.Rotate270(img)
	case 7:
		return imaging.Transverse(img)
	case 8:
		return imaging.Rotate90(img)
	}
	return img
}

func fileAvailable(path string) bool { info, e := os.Stat(path); return e == nil && info.IsDir() }

// videoThumbnail is available even when FFmpeg is not installed.
func videoThumbnail(width, height, quality int) ([]byte, error) {
	img := image.NewRGBA(image.Rect(0, 0, width, height))
	draw.Draw(img, img.Bounds(), &image.Uniform{C: color.RGBA{R: 42, G: 49, B: 59, A: 255}}, image.Point{}, draw.Src)
	size := min(width, height) / 4
	cx, cy := width/2, height/2
	for y := -size; y <= size; y++ {
		for x := -size / 2; x <= size/2; x++ {
			if x+size/2 <= (size - absInt(y)) {
				img.Set(cx+x, cy+y, color.White)
			}
		}
	}
	var out bytes.Buffer
	err := jpeg.Encode(&out, img, &jpeg.Options{Quality: quality})
	return out.Bytes(), err
}
func absInt(n int) int {
	if n < 0 {
		return -n
	}
	return n
}
