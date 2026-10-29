package main

import (
	"bufio"
	"context"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"fmt"
	"hash"
	"hash/crc32"
	"io"
	"path/filepath"
	"strings"
)

// Each format has its own versioned domain. These hashes identify encoded image
// data, not visual similarity. Unknown formats get a separately named exact-file
// identity; they must never masquerade as metadata-independent hashes.
type originalHashes struct{ ContentID, FileHash string }

func hashOriginal(ctx context.Context, source io.Reader, name string, destination io.Writer) (originalHashes, int64, error) {
	whole := sha256.New()
	writers := []io.Writer{whole}
	if destination != nil {
		writers = append(writers, destination)
	}
	counter := &hashCountingReader{r: &backupContextReader{ctx: ctx, r: source}}
	sink := &hashCheckedWriter{w: io.MultiWriter(writers...)}
	r := bufio.NewReaderSize(io.TeeReader(counter, sink), 64<<10)
	data := sha256.New()
	format := strings.ToLower(filepath.Ext(name))
	var err error
	switch format {
	case ".jpg", ".jpeg":
		err = hashJPEG(r, data)
		format = "jpeg"
	case ".png":
		err = hashPNG(r, data)
		format = "png"
	case ".webp":
		err = hashWebP(r, data)
		format = "webp"
	default:
		format = ""
	}
	// Consume trailing bytes too: file_hash always covers the complete original,
	// including metadata or padding following the image stream.
	_, drainErr := io.Copy(io.Discard, r)
	if drainErr != nil {
		return originalHashes{}, counter.n, drainErr
	}
	if sink.err != nil {
		return originalHashes{}, counter.n, sink.err
	}
	if counter.err != nil {
		return originalHashes{}, counter.n, counter.err
	}
	if ctx.Err() != nil {
		return originalHashes{}, counter.n, ctx.Err()
	}
	result := originalHashes{FileHash: "sha256:" + hex.EncodeToString(whole.Sum(nil))}
	if format == "" {
		result.ContentID = "file-sha256-v1:" + hex.EncodeToString(whole.Sum(nil))
	} else if err == nil {
		result.ContentID = "imgdata-sha256-v1:" + format + ":" + hex.EncodeToString(data.Sum(nil))
	}
	// Malformed image data is never assigned a content ID; keep its checksum for
	// diagnosis and exact-file integrity without blocking other files.
	return result, counter.n, err
}

type hashCountingReader struct {
	r   io.Reader
	n   int64
	err error
}

func (r *hashCountingReader) Read(p []byte) (int, error) {
	n, e := r.r.Read(p)
	r.n += int64(n)
	if e != nil && e != io.EOF {
		r.err = e
	}
	return n, e
}

type hashCheckedWriter struct {
	w   io.Writer
	err error
}

func (w *hashCheckedWriter) Write(p []byte) (int, error) {
	if w.err != nil {
		return 0, w.err
	}
	n, e := w.w.Write(p)
	if e == nil && n != len(p) {
		e = io.ErrShortWrite
	}
	w.err = e
	return n, e
}

func hashJPEG(r *bufio.Reader, h hash.Hash) error {
	var start [2]byte
	if _, e := io.ReadFull(r, start[:]); e != nil {
		return e
	}
	if start != [2]byte{255, 216} {
		return fmt.Errorf("invalid JPEG start")
	}
	h.Write(start[:])
	marker := byte(0)
	for {
		if marker == 0 {
			b, e := r.ReadByte()
			if e != nil {
				return e
			}
			if b != 255 {
				return fmt.Errorf("invalid JPEG marker")
			}
			for {
				marker, e = r.ReadByte()
				if e != nil {
					return e
				}
				if marker != 255 {
					break
				}
			}
		}
		m := marker
		marker = 0
		if m == 217 {
			h.Write([]byte{255, m})
			return nil
		}
		if m == 0 || m == 216 || (m >= 208 && m <= 215) {
			return fmt.Errorf("unexpected JPEG marker")
		}
		if m == 1 {
			h.Write([]byte{255, m})
			continue
		}
		var length [2]byte
		if _, e := io.ReadFull(r, length[:]); e != nil {
			return e
		}
		n := int64(binary.BigEndian.Uint16(length[:]))
		if n < 2 {
			return fmt.Errorf("invalid JPEG segment")
		}
		// EXIF, XMP, IPTC, ICC and comments are metadata. Adobe APP14 affects
		// component interpretation, so retain it conservatively.
		keep := !((m >= 224 && m <= 239 && m != 238) || m == 254)
		var w io.Writer = io.Discard
		if keep {
			h.Write([]byte{255, m})
			h.Write(length[:])
			w = h
		}
		if _, e := io.CopyN(w, r, n-2); e != nil {
			return e
		}
		if m != 218 {
			continue
		}
		// Entropy-coded data: preserve byte stuffing and restart markers. Return
		// to segment parsing for progressive/multiple-scan JPEGs.
		for {
			part, e := r.ReadSlice(255)
			if e == bufio.ErrBufferFull {
				h.Write(part)
				continue
			}
			if e != nil {
				return e
			}
			h.Write(part[:len(part)-1])
			next, e := r.ReadByte()
			if e != nil {
				return e
			}
			for next == 255 {
				next, e = r.ReadByte()
				if e != nil {
					return e
				}
			}
			if next == 0 || (next >= 208 && next <= 215) {
				h.Write([]byte{255, next})
				continue
			}
			marker = next
			break
		}
	}
}

func hashPNG(r *bufio.Reader, h hash.Hash) error {
	var magic [8]byte
	if _, e := io.ReadFull(r, magic[:]); e != nil {
		return e
	}
	if string(magic[:]) != "\x89PNG\r\n\x1a\n" {
		return fmt.Errorf("invalid PNG signature")
	}
	h.Write(magic[:])
	seenHeader, seenData := false, false
	for {
		var header [8]byte
		if _, e := io.ReadFull(r, header[:]); e != nil {
			return e
		}
		n := int64(binary.BigEndian.Uint32(header[:4]))
		kind := string(header[4:])
		if !seenHeader && (kind != "IHDR" || n != 13) {
			return fmt.Errorf("invalid PNG header")
		}
		seenHeader = true
		keep := true
		switch kind {
		case "eXIf", "tEXt", "zTXt", "iTXt", "tIME", "pHYs", "iCCP", "cHRM", "gAMA", "sRGB":
			keep = false
		}
		crc := crc32.NewIEEE()
		crc.Write(header[4:])
		var w io.Writer = crc
		if keep {
			h.Write(header[:])
			w = io.MultiWriter(h, crc)
		}
		if _, e := io.CopyN(w, r, n); e != nil {
			return e
		}
		var sum [4]byte
		if _, e := io.ReadFull(r, sum[:]); e != nil {
			return e
		}
		if binary.BigEndian.Uint32(sum[:]) != crc.Sum32() {
			return fmt.Errorf("invalid PNG checksum")
		}
		if kind == "IDAT" {
			seenData = true
		}
		if kind == "IEND" {
			if n != 0 || !seenData {
				return fmt.Errorf("invalid PNG end")
			}
			return nil
		}
	}
}

func hashWebP(r *bufio.Reader, h hash.Hash) error {
	var header [12]byte
	if _, e := io.ReadFull(r, header[:]); e != nil {
		return e
	}
	if string(header[:4]) != "RIFF" || string(header[8:]) != "WEBP" {
		return fmt.Errorf("invalid WebP header")
	}
	remaining := int64(binary.LittleEndian.Uint32(header[4:8])) - 4
	seenData := false
	for remaining > 0 {
		var chunk [8]byte
		if remaining < 8 {
			return io.ErrUnexpectedEOF
		}
		if _, e := io.ReadFull(r, chunk[:]); e != nil {
			return e
		}
		n := int64(binary.LittleEndian.Uint32(chunk[4:]))
		padded := n + n%2
		if padded > remaining-8 {
			return io.ErrUnexpectedEOF
		}
		kind := string(chunk[:4])
		keep := kind != "EXIF" && kind != "XMP " && kind != "ICCP"
		if kind == "VP8 " || kind == "VP8L" || kind == "ANMF" {
			seenData = true
		}
		var w io.Writer = io.Discard
		if keep {
			h.Write(chunk[:])
			w = h
		}
		if kind == "VP8X" {
			if n != 10 {
				return fmt.Errorf("invalid WebP extended header")
			}
			flag, e := r.ReadByte()
			if e != nil {
				return e
			}
			h.Write([]byte{flag &^ 0x2c}) // ICC, EXIF, XMP presence
			if _, e = io.CopyN(h, r, n-1); e != nil {
				return e
			}
		} else if _, e := io.CopyN(w, r, n); e != nil {
			return e
		}
		if n%2 != 0 {
			if _, e := r.ReadByte(); e != nil {
				return e
			}
		}
		remaining -= 8 + padded
	}
	if !seenData {
		return fmt.Errorf("missing WebP image data")
	}
	return nil
}
