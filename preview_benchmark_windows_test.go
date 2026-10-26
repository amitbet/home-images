//go:build windows && previewbench

package main

// Manual benchmark bridge: compile with go test -tags previewbench -c, then set HOMEIMAGES_BENCH_ROOT
// and HOMEIMAGES_BENCH_TURBO_DLL. Reads JSON lines on stdin; never opens a database.
import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"github.com/disintegration/imaging"
	"golang.org/x/sys/windows"
	"image"
	"image/draw"
	"image/jpeg"
	"os"
	"runtime"
	"testing"
	"time"
	"unsafe"
)

type previewBenchRequest struct {
	Method, Path, Output   string
	Width, Height, Quality int
}
type previewBenchResponse struct {
	MS                   float64
	Bytes, Width, Height int
	Error                string
}

func TestPreviewBenchmarkBridge(t *testing.T) {
	rootPath := os.Getenv("HOMEIMAGES_BENCH_ROOT")
	if rootPath == "" {
		t.Skip("manual benchmark")
	}
	root, err := os.OpenRoot(rootPath)
	if err != nil {
		t.Fatal(err)
	}
	defer root.Close()
	l := &Library{root: root, rootPath: rootPath, config: Config{Root: rootPath, MaxPixels: 80_000_000, FFmpeg: "ffmpeg"}}
	dll := windows.NewLazyDLL(os.Getenv("HOMEIMAGES_BENCH_TURBO_DLL"))
	scanner := bufio.NewScanner(os.Stdin)
	for scanner.Scan() {
		var req previewBenchRequest
		var res previewBenchResponse
		if err = json.Unmarshal(scanner.Bytes(), &req); err != nil {
			t.Fatal(err)
		}
		start := time.Now()
		var data []byte
		if req.Method == "go" {
			data, err = l.renderJPEG(context.Background(), Photo{Path: req.Path}, req.Width, req.Height, req.Quality)
		} else {
			data, err = benchTurbo(l, dll, req)
		}
		res.MS = float64(time.Since(start).Microseconds()) / 1000
		if err != nil {
			res.Error = err.Error()
		} else {
			res.Bytes = len(data)
			cfg, e := jpeg.DecodeConfig(bytes.NewReader(data))
			if e != nil {
				res.Error = e.Error()
			} else {
				res.Width, res.Height = cfg.Width, cfg.Height
			}
			if req.Output != "" {
				if e = os.WriteFile(req.Output, data, 0600); e != nil {
					res.Error = e.Error()
				}
			}
		}
		encoded, _ := json.Marshal(res)
		fmt.Println("BENCH " + string(encoded))
	}
}
func benchTurbo(l *Library, dll *windows.LazyDLL, req previewBenchRequest) ([]byte, error) {
	p, err := l.metadata(context.Background(), req.Path)
	if err != nil {
		return nil, err
	}
	data, err := os.ReadFile(p.AbsolutePath)
	if err != nil {
		return nil, err
	}
	init := dll.NewProc("tjInitDecompress")
	h, _, _ := init.Call()
	if h == 0 {
		return nil, fmt.Errorf("tjInitDecompress failed")
	}
	defer dll.NewProc("tjDestroy").Call(h)
	check := func(code uintptr) error {
		if int32(code) != 0 {
			return fmt.Errorf("TurboJPEG error %d", int32(code))
		}
		return nil
	}
	var w, hgt, subsamp, colorspace int32
	code, _, _ := dll.NewProc("tjDecompressHeader3").Call(h, uintptr(unsafe.Pointer(&data[0])), uintptr(len(data)), uintptr(unsafe.Pointer(&w)), uintptr(unsafe.Pointer(&hgt)), uintptr(unsafe.Pointer(&subsamp)), uintptr(unsafe.Pointer(&colorspace)))
	if err = check(code); err != nil {
		return nil, err
	}
	targetW, targetH := req.Width, req.Height
	if p.Orientation >= 5 && p.Orientation <= 8 {
		targetW, targetH = targetH, targetW
	}
	desiredW, desiredH := int(w), int(hgt)
	if desiredW > targetW || desiredH > targetH {
		ratio := min(float64(targetW)/float64(w), float64(targetH)/float64(hgt))
		desiredW = max(1, int(float64(w)*ratio))
		desiredH = max(1, int(float64(hgt)*ratio))
	}
	dw, dh := int(w), int(hgt)
	if req.Method != "turbo-full" {
		var count int32
		ptr, _, _ := dll.NewProc("tjGetScalingFactors").Call(uintptr(unsafe.Pointer(&count)))
		type factor struct{ Num, Den int32 }
		for _, f := range unsafe.Slice((*factor)(*(*unsafe.Pointer)(unsafe.Pointer(&ptr))), count) {
			sw := (int(w)*int(f.Num) + int(f.Den) - 1) / int(f.Den)
			sh := (int(hgt)*int(f.Num) + int(f.Den) - 1) / int(f.Den)
			if sw >= desiredW && sh >= desiredH && sw*sh < dw*dh {
				dw, dh = sw, sh
			}
		}
	}
	img := image.NewNRGBA(image.Rect(0, 0, dw, dh))
	code, _, _ = dll.NewProc("tjDecompress2").Call(h, uintptr(unsafe.Pointer(&data[0])), uintptr(len(data)), uintptr(unsafe.Pointer(&img.Pix[0])), uintptr(dw), uintptr(img.Stride), uintptr(dh), 7, 0)
	runtime.KeepAlive(data)
	if int32(code) != 0 {
		// libjpeg-turbo can finish decoding while reporting a recoverable warning.
		// The scan-header warning in this sample set is also tolerated by Go/Pillow.
		severity, _, _ := dll.NewProc("tjGetErrorCode").Call(h)
		if severity != 0 {
			return nil, fmt.Errorf("TurboJPEG fatal decode error")
		}
	}

	resized := imaging.Fit(orient(img, p.Orientation), req.Width, req.Height, imaging.Lanczos)
	rgba := image.NewRGBA(resized.Bounds())
	draw.Draw(rgba, rgba.Bounds(), resized, resized.Bounds().Min, draw.Src)
	comp, _, _ := dll.NewProc("tjInitCompress").Call()
	if comp == 0 {
		return nil, fmt.Errorf("tjInitCompress failed")
	}
	defer dll.NewProc("tjDestroy").Call(comp)
	var out unsafe.Pointer
	var size uint32
	code, _, _ = dll.NewProc("tjCompress2").Call(comp, uintptr(unsafe.Pointer(&rgba.Pix[0])), uintptr(rgba.Bounds().Dx()), uintptr(rgba.Stride), uintptr(rgba.Bounds().Dy()), 7, uintptr(unsafe.Pointer(&out)), uintptr(unsafe.Pointer(&size)), 2, uintptr(req.Quality), 0)
	runtime.KeepAlive(rgba)
	if out != nil {
		defer dll.NewProc("tjFree").Call(uintptr(out))
	}
	if err = check(code); err != nil {
		return nil, err
	}
	return bytes.Clone(unsafe.Slice((*byte)(out), int(size))), nil
}
