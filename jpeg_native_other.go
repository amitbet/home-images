//go:build !windows || !amd64

package main

import (
	"context"
	"fmt"
	"os"
)

func nativeJPEGName() string { return "go" }
func nativeJPEGPreview(context.Context, *os.File, Photo, int, int, int, int64, ...*faceCrop) ([]byte, error) {
	return nil, fmt.Errorf("native JPEG decoder unavailable on this platform")
}
