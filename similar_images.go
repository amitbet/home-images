package main

import (
	"context"
	"database/sql"
	"encoding/hex"
	"errors"
	"net/http"
	"strings"
)

func validateImageSearch(r *http.Request) error {
	id := r.URL.Query().Get("similar_to")
	if id == "" {
		return nil
	}
	if _, err := hex.DecodeString(id); err != nil || len(id) != 24 {
		return bad(400, "Invalid source photo")
	}
	if r.URL.Query().Get("mode") == "metadata" || strings.TrimSpace(r.URL.Query().Get("q")) != "" {
		return bad(400, "Choose either a text search or a similar-photo search")
	}
	return nil
}

// Reuse the current indexed embedding. No image decoding or ML worker is needed.
func (a *AI) imageQueryVector(ctx context.Context, id string) (vector, error) {
	var blob []byte
	err := a.read.QueryRowContext(ctx, `SELECT a.clip FROM ai_images a JOIN catalog.images i ON i.id=a.image_id AND i.signature=a.signature WHERE a.image_id=? AND a.models=? AND a.error=''`, id, a.models).Scan(&blob)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, bad(404, "This photo has no current search embedding yet. Index it before finding similar photos.")
	}
	if err != nil {
		return nil, err
	}
	v := vectorBlob(blob)
	if len(v) < 2 {
		return nil, bad(503, "This photo has no usable search embedding yet")
	}
	return v, nil
}
