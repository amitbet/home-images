package main

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"time"

	"github.com/amitbet/home-images/internal/cliptext"
)

func (a *AI) encodeQuery(ctx context.Context, q string) (vector, error) {
	if a.lib == nil || a.lib.config.ML.TextEncoder == "remote" {
		return a.remoteTextVector(ctx, q)
	}
	if a.lib.config.ML.ClipModel != cliptext.Model {
		return nil, bad(503, "Local text search supports ViT-B-32__openai. Use ml.text_encoder=remote for a different indexed model.")
	}
	a.textMu.Lock()
	defer a.textMu.Unlock()
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if a.localText == nil {
		directory := a.lib.config.ML.TextModelDirectory
		if directory == "" {
			exe, err := os.Executable()
			if err != nil {
				return nil, err
			}
			exe, err = filepath.EvalSymlinks(exe)
			if err != nil {
				return nil, err
			}
			directory = filepath.Join(filepath.Dir(exe), "local-text")
		}
		encoder, err := cliptext.Open(directory, min(4, runtime.NumCPU()))
		if err != nil {
			return nil, bad(503, fmt.Sprintf("Local text encoder is unavailable: %v. Install the local-text assets with scripts/package-local-text.ps1.", err))
		}
		a.localText = encoder
	}
	values, err := a.localText.Encode(ctx, q)
	return vector(values), err
}
func (a *AI) remoteTextVector(ctx context.Context, q string) (vector, error) {
	ctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	pred, err := a.ml.predict(ctx, nil, q)
	if err != nil {
		return nil, bad(503, "Content search could not reach the ML worker or it took too long. Try again when the worker is ready; your face filters are unchanged.")
	}
	return parseVector(pred.Clip)
}
