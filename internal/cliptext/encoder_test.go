package cliptext

import (
	"context"
	"encoding/json"
	"math"
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

func assets(t *testing.T) string {
	t.Helper()
	directory := os.Getenv("HOMEIMAGES_TEST_TEXT_ASSETS")
	if directory == "" {
		t.Skip("set HOMEIMAGES_TEST_TEXT_ASSETS to verified local-text directory")
	}
	return directory
}
func TestTokenizerMatchesImmich(t *testing.T) {
	tokenizer, err := LoadTokenizer(filepath.Join(assets(t), "tokenizer.json"))
	if err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile("testdata/tokens.json")
	if err != nil {
		t.Fatal(err)
	}
	var cases []struct {
		Text string
		IDs  []int32
	}
	if err = json.Unmarshal(raw, &cases); err != nil {
		t.Fatal(err)
	}
	for _, c := range cases {
		t.Run(c.Text, func(t *testing.T) {
			ids, err := tokenizer.Encode(c.Text)
			if err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(ids[:], c.IDs) {
				t.Fatalf("tokens differ: got %v want %v", ids, c.IDs)
			}
		})
	}
}
func TestEncoderLocalInference(t *testing.T) {
	encoder, err := Open(assets(t), 4)
	if err != nil {
		t.Fatal(err)
	}
	defer encoder.Close()
	values, err := encoder.Encode(context.Background(), "tree")
	if err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile("testdata/tree-vector.json")
	if err != nil {
		t.Fatal(err)
	}
	var reference []float32
	if err = json.Unmarshal(raw, &reference); err != nil {
		t.Fatal(err)
	}
	for i, x := range values {
		if i >= len(reference) || math.Abs(float64(x-reference[i])) > 1e-5 {
			t.Fatalf("local embedding differs from Immich at component %d", i)
		}
	}
	if len(values) != 512 {
		t.Fatal(len(values))
	}
	var norm float64
	for _, x := range values {
		norm += float64(x) * float64(x)
	}
	if math.Abs(norm-1) > 1e-5 {
		t.Fatal("not normalized", norm)
	}
	encoder.Close()
	if _, err = encoder.Encode(context.Background(), "tree"); err == nil {
		t.Fatal("closed session accepted query")
	}
}
