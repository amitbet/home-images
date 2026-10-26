package main

// The Immich ML service is an inference engine, not an index. Keep its internal
// wire protocol here so upgrading a pinned container has one compatibility seam.
import (
	"bytes"
	"context"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"mime/multipart"
	"net/http"
	"strings"
	"time"
)

type vector []float32

func parseVector(raw json.RawMessage) (vector, error) {
	if len(raw) > 0 && raw[0] == '"' {
		var s string
		if err := json.Unmarshal(raw, &s); err != nil {
			return nil, err
		}
		raw = []byte(s)
	}
	var v vector
	if err := json.Unmarshal(raw, &v); err != nil {
		return nil, err
	}
	if len(v) < 2 || len(v) > 8192 {
		return nil, fmt.Errorf("invalid embedding dimension %d", len(v))
	}
	var norm float64
	for _, x := range v {
		if math.IsNaN(float64(x)) || math.IsInf(float64(x), 0) {
			return nil, fmt.Errorf("nonfinite embedding")
		}
		norm += float64(x) * float64(x)
	}
	if norm == 0 {
		return nil, fmt.Errorf("empty embedding")
	}
	for i := range v {
		v[i] /= float32(math.Sqrt(norm))
	}
	return v, nil
}
func (v vector) blob() []byte {
	b := make([]byte, 4*len(v))
	for i, x := range v {
		binary.LittleEndian.PutUint32(b[i*4:], math.Float32bits(x))
	}
	return b
}
func vectorBlob(b []byte) vector {
	if len(b)%4 != 0 {
		return nil
	}
	v := make(vector, len(b)/4)
	for i := range v {
		v[i] = math.Float32frombits(binary.LittleEndian.Uint32(b[i*4:]))
	}
	return v
}
func similarity(a, b vector) float32 {
	if len(a) != len(b) {
		return -1
	}
	var n float32
	for i := range a {
		n += a[i] * b[i]
	}
	return n
}

type faceBox struct {
	X1 float64 `json:"x1"`
	Y1 float64 `json:"y1"`
	X2 float64 `json:"x2"`
	Y2 float64 `json:"y2"`
}
type mlFace struct {
	Box       faceBox         `json:"boundingBox"`
	Embedding json.RawMessage `json:"embedding"`
	Score     float32         `json:"score"`
}
type mlPrediction struct {
	Clip   json.RawMessage `json:"clip"`
	Faces  []mlFace        `json:"facial-recognition"`
	Width  int             `json:"imageWidth"`
	Height int             `json:"imageHeight"`
}
type mlClient struct {
	config MLConfig
	http   *http.Client
}

func newML(c MLConfig) *mlClient {
	return &mlClient{c, &http.Client{Timeout: 180 * time.Second, Transport: &http.Transport{MaxIdleConnsPerHost: 6, MaxConnsPerHost: 6, ResponseHeaderTimeout: 175 * time.Second}}}
}
func (m *mlClient) predict(ctx context.Context, image []byte, text string) (mlPrediction, error) {
	var out mlPrediction
	var body bytes.Buffer
	w := multipart.NewWriter(&body)
	slot := func(name string) map[string]any {
		return map[string]any{"modelName": name, "options": map[string]any{}}
	}
	entries := map[string]any{}
	if image != nil {
		entries["clip"] = map[string]any{"visual": slot(m.config.ClipModel)}
		entries["facial-recognition"] = map[string]any{"detection": map[string]any{"modelName": m.config.FaceModel, "options": map[string]any{"minScore": 0.8}}, "recognition": slot(m.config.FaceModel)}
		p, err := w.CreateFormFile("image", "preview.jpg")
		if err != nil {
			return out, err
		}
		if _, err = p.Write(image); err != nil {
			return out, err
		}
	} else {
		entries["clip"] = map[string]any{"textual": slot(m.config.ClipModel)}
		if err := w.WriteField("text", text); err != nil {
			return out, err
		}
	}
	data, _ := json.Marshal(entries)
	if err := w.WriteField("entries", string(data)); err != nil {
		return out, err
	}
	if err := w.Close(); err != nil {
		return out, err
	}
	req, err := http.NewRequestWithContext(ctx, "POST", strings.TrimRight(m.config.URL, "/")+"/predict", &body)
	if err != nil {
		return out, err
	}
	req.Header.Set("Content-Type", w.FormDataContentType())
	resp, err := m.http.Do(req)
	if err != nil {
		return out, fmt.Errorf("Mac ML unavailable: %w", err)
	}
	defer resp.Body.Close()
	data, err = io.ReadAll(io.LimitReader(resp.Body, 8<<20))
	if err != nil {
		return out, err
	}
	if resp.StatusCode != 200 {
		return out, fmt.Errorf("Mac ML returned HTTP %d: %.300s", resp.StatusCode, data)
	}
	if err = json.Unmarshal(data, &out); err != nil {
		return out, fmt.Errorf("invalid ML response: %w", err)
	}
	return out, nil
}
