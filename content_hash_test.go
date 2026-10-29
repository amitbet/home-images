package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/binary"
	"fmt"
	"hash/crc32"
	"image"
	"image/color"
	"image/jpeg"
	"image/png"
	"io"
	"os"
	"testing"
)

func hashTestData(t *testing.T, data []byte, name string) originalHashes {
	t.Helper()
	var out bytes.Buffer
	h, n, err := hashOriginal(context.Background(), bytes.NewReader(data), name, &out)
	if err != nil || n != int64(len(data)) || !bytes.Equal(out.Bytes(), data) {
		t.Fatalf("hash/copy: n=%d err=%v", n, err)
	}
	if h.FileHash != fmt.Sprintf("sha256:%x", sha256.Sum256(data)) {
		t.Fatal("wrong whole-file checksum")
	}
	return h
}
func jpegMetadata(data []byte, payload string) []byte {
	segment := []byte{255, 225, 0, 0}
	binary.BigEndian.PutUint16(segment[2:], uint16(len(payload)+2))
	result := append([]byte{}, data[:2]...)
	result = append(result, segment...)
	result = append(result, payload...)
	return append(result, data[2:]...)
}
func TestJPEGIdentityExcludesMetadataAndPreservesImageData(t *testing.T) {
	original, err := os.ReadFile("testdata/orientation6.jpg")
	if err != nil {
		t.Fatal(err)
	}
	a := hashTestData(t, original, "original.jpg")
	b := hashTestData(t, jpegMetadata(original, "Exif\x00\x00different GPS and date"), "renamed.jpeg")
	if a.ContentID == "" || a.ContentID != b.ContentID || a.FileHash == b.FileHash {
		t.Fatalf("metadata identities: %+v %+v", a, b)
	}
	// Recompression/image edits are separate identities.
	var changed bytes.Buffer
	im := image.NewRGBA(image.Rect(0, 0, 4, 4))
	im.Set(0, 0, color.White)
	jpeg.Encode(&changed, im, nil)
	if hashTestData(t, changed.Bytes(), "edited.jpg").ContentID == a.ContentID {
		t.Fatal("image change not detected")
	}
	// Multiple scans and stuffed/restart bytes must all participate, while APP1
	// between scans remains excluded.
	scans := []byte{255, 216, 255, 218, 0, 2, 1, 255, 0, 2, 255, 208, 3, 255, 225, 0, 3, 9, 255, 218, 0, 2, 4, 255, 217}
	edited := append([]byte{}, scans...)
	edited[22] = 5
	if hashTestData(t, scans, "p.jpg").ContentID == hashTestData(t, edited, "p.jpg").ContentID {
		t.Fatal("later scan omitted")
	}
}
func pngTestChunk(kind string, payload []byte) []byte {
	b := make([]byte, 8)
	binary.BigEndian.PutUint32(b, uint32(len(payload)))
	copy(b[4:], kind)
	b = append(b, payload...)
	sum := make([]byte, 4)
	binary.BigEndian.PutUint32(sum, crc32.ChecksumIEEE(b[4:]))
	return append(b, sum...)
}
func TestPNGIdentityExcludesMetadata(t *testing.T) {
	var b bytes.Buffer
	png.Encode(&b, image.NewRGBA(image.Rect(0, 0, 4, 4)))
	original := b.Bytes()
	metadata := pngTestChunk("eXIf", []byte("GPS/date"))
	changed := append([]byte{}, original[:33]...)
	changed = append(changed, metadata...)
	changed = append(changed, original[33:]...)
	a, c := hashTestData(t, original, "a.png"), hashTestData(t, changed, "b.png")
	if a.ContentID != c.ContentID || a.FileHash == c.FileHash {
		t.Fatal("PNG metadata affects identity")
	}
	changed[41] ^= 1
	h, _, err := hashOriginal(context.Background(), bytes.NewReader(changed), "bad.png", nil)
	if err == nil || h.ContentID != "" {
		t.Fatal("accepted corrupt PNG CRC")
	}
}
func webpTestChunk(kind string, p []byte) []byte {
	b := make([]byte, 8)
	copy(b, kind)
	binary.LittleEndian.PutUint32(b[4:], uint32(len(p)))
	b = append(b, p...)
	if len(p)%2 != 0 {
		b = append(b, 0)
	}
	return b
}
func webpTestFile(metadata string) []byte {
	flag := byte(0)
	if metadata != "" {
		flag = 0x2c
	}
	data := webpTestChunk("VP8X", append([]byte{flag}, make([]byte, 9)...))
	data = append(data, webpTestChunk("VP8 ", []byte{1, 2, 3})...)
	if metadata != "" {
		data = append(data, webpTestChunk("EXIF", []byte(metadata))...)
		data = append(data, webpTestChunk("XMP ", []byte(metadata))...)
		data = append(data, webpTestChunk("ICCP", []byte(metadata))...)
	}
	header := []byte("RIFF\x00\x00\x00\x00WEBP")
	binary.LittleEndian.PutUint32(header[4:], uint32(len(data)+4))
	return append(header, data...)
}
func TestWebPIdentityExcludesMetadata(t *testing.T) {
	a := hashTestData(t, webpTestFile(""), "a.webp")
	b := hashTestData(t, webpTestFile("metadata"), "b.webp")
	if a.ContentID != b.ContentID || a.FileHash == b.FileHash {
		t.Fatal("WebP metadata affects identity")
	}
}

type failHashWriter struct{}

func (failHashWriter) Write([]byte) (int, error) { return 0, io.ErrClosedPipe }

type failHashReader struct{ sent bool }

func (r *failHashReader) Read(p []byte) (int, error) {
	if r.sent {
		return 0, io.EOF
	}
	r.sent = true
	copy(p, "bad")
	return 3, io.ErrUnexpectedEOF
}
func TestOriginalHashErrorsAndFallback(t *testing.T) {
	h := hashTestData(t, []byte("opaque data"), "image.heic")
	if h.ContentID != "file-sha256-v1:"+h.FileHash[len("sha256:"):] {
		t.Fatal("fallback mislabeled")
	}
	for _, name := range []string{"bad.jpg", "bad.png", "bad.webp"} {
		h, _, err := hashOriginal(context.Background(), bytes.NewReader([]byte("bad")), name, nil)
		if err == nil || h.ContentID != "" || h.FileHash == "" {
			t.Fatalf("malformed %s: %+v %v", name, h, err)
		}
	}
	h, _, err := hashOriginal(context.Background(), bytes.NewReader([]byte("bad")), "bad.jpg", failHashWriter{})
	if err == nil || h.FileHash != "" {
		t.Fatal("write failure returned usable checksum")
	}
	h, _, err = hashOriginal(context.Background(), &failHashReader{}, "bad.jpg", nil)
	if err == nil || h.FileHash != "" {
		t.Fatal("read failure returned usable checksum")
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	h, _, err = hashOriginal(ctx, bytes.NewReader([]byte("data")), "a.jpg", nil)
	if err != context.Canceled || h.FileHash != "" {
		t.Fatal("cancellation ignored")
	}
}
