package main

// Opt-in experiment: this only opens benchmark snapshots, never the live DB.
import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestVectorIndexBenchmark(t *testing.T) {
	dir := os.Getenv("HOME_IMAGES_VECTOR_BENCH")
	if dir == "" {
		t.Skip("set HOME_IMAGES_VECTOR_BENCH to the prepared snapshot directory")
	}
	read := func(name string, v any) {
		b, e := os.ReadFile(filepath.Join(dir, name))
		if e != nil {
			t.Fatal(e)
		}
		if e = json.Unmarshal(b, v); e != nil {
			t.Fatal(e)
		}
	}
	var manifest struct{ Model string }
	var queries []struct {
		Text   string
		Vector json.RawMessage
	}
	var cases []struct {
		Name, Clause string
		Args         []any
		K            int
		Eligible     int
	}
	read("manifest.json", &manifest)
	read("queries.json", &queries)
	read("cases.json", &cases)
	a := &AI{lib: &Library{config: Config{ML: MLConfig{URL: "benchmark-cached-query"}}}, models: manifest.Model, textCache: map[string]vector{}, searchSlot: make(chan struct{}, 1)}
	a.read = openSQLite("file:"+filepath.ToSlash(filepath.Join(dir, "ai.sqlite3"))+"?mode=ro", map[string]string{"catalog": "file:" + filepath.ToSlash(filepath.Join(dir, "index-go.sqlite3")) + "?mode=ro"}, true)
	defer a.read.Close()
	for _, q := range queries {
		v, e := parseVector(q.Vector)
		if e != nil {
			t.Fatal(e)
		}
		a.textCache[q.Text] = v
	}
	type sample struct {
		Case  string
		Query int
		MS    float64
		Total int
		Hits  []ranked
	}
	results := []sample{}
	optimized := os.Getenv("HOME_IMAGES_VECTOR_BENCH_OPTIMIZED") == "1"
	if optimized {
		catalog := openSQLite(filepath.Join(dir, "index-go.sqlite3"), nil, false)
		_, err := catalog.Exec("DROP INDEX IF EXISTS bench_directory_exact; CREATE INDEX IF NOT EXISTS image_directory_exact ON images(directory,id,signature); PRAGMA main.optimize=0x10002")
		catalog.Close()
		if err != nil {
			t.Fatal(err)
		}
		ai := openSQLite(filepath.Join(dir, "ai.sqlite3"), nil, false)
		_, err = ai.Exec("PRAGMA main.optimize=0x10002")
		ai.Close()
		if err != nil {
			t.Fatal(err)
		}
	}
	for _, c := range cases {
		if optimized && c.Name == "person_top60" {
			c.Clause = currentPersonImages
		}
		warm, e := a.rankSelection(context.Background(), queries[0].Text, c.Clause, c.Args, c.K)
		if e != nil {
			t.Fatal(e)
		}
		if warm.Total != c.Eligible {
			t.Fatalf("%s: got %d candidates, expected %d", c.Name, warm.Total, c.Eligible)
		}
		for j, q := range queries {
			start := time.Now()
			result, e := a.rankSelection(context.Background(), q.Text, c.Clause, c.Args, c.K)
			elapsed := float64(time.Since(start).Microseconds()) / 1000
			if e != nil {
				t.Fatal(e)
			}
			results = append(results, sample{c.Name, j, elapsed, result.Total, result.Hits})
			t.Logf("%s query=%d candidates=%d ms=%.2f", c.Name, j, result.Total, elapsed)
		}
	}
	b, e := json.MarshalIndent(results, "", "  ")
	if e != nil {
		t.Fatal(e)
	}
	output := "go-results.json"
	if optimized {
		output = "go-results-optimized.json"
	}
	if e = os.WriteFile(filepath.Join(dir, output), b, 0600); e != nil {
		t.Fatal(e)
	}
}
