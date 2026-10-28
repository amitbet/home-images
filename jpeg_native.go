//go:build (windows || linux || darwin) && (amd64 || arm64)

package main

import (
	"bytes"
	"context"
	"fmt"
	"image"
	"image/draw"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"sync"
	"unsafe"

	"github.com/disintegration/imaging"
)

var turboOnce sync.Once
var turboDefault *turboJPEG
var turboSymbols = []string{"tjInitDecompress", "tjInitCompress", "tjDestroy", "tjDecompressHeader3", "tjGetScalingFactors", "tjDecompress2", "tjCompress2", "tjFree", "tjGetErrorCode", "tjGetErrorStr2"}

// Only load an absolute, application-owned library beside the executable.
// The installers bundle it; PATH/CWD and system codec versions are not searched.
func defaultTurboJPEG() *turboJPEG {
	turboOnce.Do(func() {
		exe, err := os.Executable()
		if err == nil {
			exe, err = filepath.EvalSymlinks(exe)
		}
		if err == nil {
			turboDefault, _ = loadTurboJPEG(filepath.Join(filepath.Dir(exe), turboLibraryName()))
		}
	})
	return turboDefault
}
func nativeJPEGName() string {
	if defaultTurboJPEG() != nil {
		return "turbojpeg"
	}
	return "go"
}
func nativeJPEGPreview(ctx context.Context, f *os.File, p Photo, width, height, quality int, maxPixels int64, crop ...*faceCrop) ([]byte, error) {
	lib := defaultTurboJPEG()
	if lib == nil {
		return nil, fmt.Errorf("TurboJPEG unavailable")
	}
	return lib.preview(ctx, f, p, width, height, quality, maxPixels, crop...)
}

func (lib *turboJPEG) preview(ctx context.Context, f *os.File, p Photo, width, height, quality int, maxPixels int64, crop ...*faceCrop) ([]byte, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if width < 1 || height < 1 || width > 2560 || height > 2560 || quality < 40 || quality > 95 {
		return nil, fmt.Errorf("invalid native preview request")
	}
	const maxInput = 128 << 20
	info, err := f.Stat()
	if err != nil {
		return nil, err
	}
	if info.Size() < 4 || info.Size() > maxInput {
		return nil, fmt.Errorf("native JPEG input limit")
	}
	data, err := io.ReadAll(io.LimitReader(f, maxInput+1))
	if err != nil {
		return nil, err
	}
	if len(data) < 4 || len(data) > maxInput {
		return nil, fmt.Errorf("native JPEG input limit")
	}
	handle := lib.call("tjInitDecompress")
	if handle == 0 {
		return nil, fmt.Errorf("native JPEG initialization failed")
	}
	defer lib.call("tjDestroy", handle)
	var w, h, subsampling, colorspace int32
	code := lib.call("tjDecompressHeader3", handle, uintptr(unsafe.Pointer(&data[0])), uintptr(len(data)), uintptr(unsafe.Pointer(&w)), uintptr(unsafe.Pointer(&h)), uintptr(unsafe.Pointer(&subsampling)), uintptr(unsafe.Pointer(&colorspace)))
	runtime.KeepAlive(data)
	if int32(code) != 0 || w < 1 || h < 1 || maxPixels < 1 || int64(w) > maxPixels/int64(h) {
		return nil, fmt.Errorf("invalid or oversized native JPEG header")
	}
	// CMYK/YCCK retain Go's existing color-management behavior.
	if colorspace == 3 || colorspace == 4 {
		return nil, fmt.Errorf("native JPEG color space fallback")
	}
	tw, th := width, height
	if p.Orientation >= 5 && p.Orientation <= 8 {
		tw, th = th, tw
	}
	ratio := min(1.0, min(float64(tw)/float64(w), float64(th)/float64(h)))
	desiredW, desiredH := max(1, int(float64(w)*ratio)), max(1, int(float64(h)*ratio))
	dw, dh := int(w), int(h)
	var count int32
	pointer := lib.call("tjGetScalingFactors", uintptr(unsafe.Pointer(&count)))
	if pointer == 0 || count < 1 || count > 64 {
		return nil, fmt.Errorf("invalid native JPEG scaling table")
	}
	type factor struct{ Num, Den int32 }
	// The native allocation is not managed/moved by Go. Reinterpret the returned
	// address without constructing a Go pointer by arithmetic on uintptr.
	factors := unsafe.Slice((*factor)(*(*unsafe.Pointer)(unsafe.Pointer(&pointer))), int(count))
	for _, f := range factors {
		if f.Num < 1 || f.Den < 1 || f.Num > 16 || f.Den > 16 {
			continue
		}
		sw := (int(w)*int(f.Num) + int(f.Den) - 1) / int(f.Den)
		sh := (int(h)*int(f.Num) + int(f.Den) - 1) / int(f.Den)
		if sw >= desiredW && sh >= desiredH && sw*sh < dw*dh {
			dw, dh = sw, sh
		}
	}
	// Conservative progressive 4:4:4 coefficient estimate plus Go resize buffers.
	// Native allocations are outside GOMEMLIMIT, so refuse excessive working sets.
	working := int64(cap(data)) + int64(w)*int64(h)*6 + int64(dw)*int64(dh)*8 + int64(desiredW)*int64(desiredH)*16
	if working > 192<<20 {
		return nil, fmt.Errorf("native JPEG memory budget exceeded")
	}
	if err = ctx.Err(); err != nil {
		return nil, err
	}
	decoded := image.NewNRGBA(image.Rect(0, 0, dw, dh))
	code = lib.call("tjDecompress2", handle, uintptr(unsafe.Pointer(&data[0])), uintptr(len(data)), uintptr(unsafe.Pointer(&decoded.Pix[0])), uintptr(dw), uintptr(decoded.Stride), uintptr(dh), 7, 0)
	runtime.KeepAlive(data)
	runtime.KeepAlive(decoded)
	if int32(code) != 0 {
		if lib.call("tjGetErrorCode", handle) != 0 {
			return nil, fmt.Errorf("native JPEG decode failed")
		}
		pointer := lib.call("tjGetErrorStr2", handle)
		message := turboString(pointer)
		// Only the benign camera warning verified against Go in the benchmark is
		// accepted. Truncation and other warnings go through the existing decoder.
		if message != "Invalid SOS parameters for sequential JPEG" {
			return nil, fmt.Errorf("native JPEG warning: %s", message)
		}
	}

	if err = ctx.Err(); err != nil {
		return nil, err
	}
	var resized image.Image
	if len(crop) > 0 && crop[0] != nil {
		resized, err = cropFace(orient(decoded, p.Orientation), crop[0])
		if err != nil {
			return nil, err
		}
	} else {
		resized = imaging.Fit(orient(decoded, p.Orientation), width, height, imaging.Lanczos)
	}
	rgba := image.NewRGBA(resized.Bounds())
	draw.Draw(rgba, rgba.Bounds(), resized, resized.Bounds().Min, draw.Src)
	compressor := lib.call("tjInitCompress")
	if compressor == 0 {
		return nil, fmt.Errorf("native JPEG encoder initialization failed")
	}
	defer lib.call("tjDestroy", compressor)
	var output unsafe.Pointer
	// C unsigned long is 32-bit on Windows and 64-bit on our Unix targets.
	var size turboSize
	code = lib.call("tjCompress2", compressor, uintptr(unsafe.Pointer(&rgba.Pix[0])), uintptr(rgba.Bounds().Dx()), uintptr(rgba.Stride), uintptr(rgba.Bounds().Dy()), 7, uintptr(unsafe.Pointer(&output)), uintptr(unsafe.Pointer(&size)), 2, uintptr(quality), 0)
	runtime.KeepAlive(rgba)
	if output != nil {
		defer lib.call("tjFree", uintptr(output))
	}
	if int32(code) != 0 || output == nil || size < 4 || size > 32<<20 {
		return nil, fmt.Errorf("native JPEG encode failed")
	}
	if err = ctx.Err(); err != nil {
		return nil, err
	}
	return bytes.Clone(unsafe.Slice((*byte)(output), int(size))), nil
}

func turboString(address uintptr) string {
	if address == 0 {
		return ""
	}
	pointer := *(*unsafe.Pointer)(unsafe.Pointer(&address))
	message := make([]byte, 0, 128)
	for i := 0; i < 4096; i++ {
		b := *(*byte)(unsafe.Add(pointer, i))
		if b == 0 {
			break
		}
		message = append(message, b)
	}
	return string(message)
}
