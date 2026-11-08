//go:build (!windows && !linux && !darwin) || (!amd64 && !arm64)

package cliptext

import (
	"context"
	"fmt"
)

type Encoder struct{}

func LibraryName() string { return "unsupported" }
func Open(string, int) (*Encoder, error) {
	return nil, fmt.Errorf("local text encoder is unsupported on this platform")
}
func (*Encoder) Encode(context.Context, string) ([]float32, error) {
	return nil, fmt.Errorf("local text encoder is unsupported on this platform")
}
func (*Encoder) Close() {}
