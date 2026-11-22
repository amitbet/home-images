package main

import (
	"bufio"
	"encoding/binary"
	"io"
	"math"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"testing"
	"time"
)

// TestPeopleDiskBenchmark compares graph lookup with an exact, sequential
// on-disk vector scan. It only reads the supplied People snapshot.
func TestPeopleDiskBenchmark(t *testing.T) {
	path := os.Getenv("HOME_IMAGES_PEOPLE_BENCH")
	if path == "" {
		t.Skip("set HOME_IMAGES_PEOPLE_BENCH to a people.hnsw snapshot")
	}
	const queryCount, k = 20, 10
	heap := func() uint64 {
		runtime.GC()
		var stats runtime.MemStats
		runtime.ReadMemStats(&stats)
		return stats.HeapAlloc
	}
	baseline := heap()
	start := time.Now()
	saved, err := readPersonHNSW(path)
	if err != nil {
		t.Fatal(err)
	}
	index, err := restorePersonHNSW(saved)
	if err != nil {
		t.Fatal(err)
	}
	loadTime := time.Since(start)
	counts := make(map[int]int)
	dim := 0
	for _, rep := range saved.Representatives {
		n := len(rep.Vector)
		counts[n]++
		if counts[n] > counts[dim] {
			dim = n
		}
	}
	if counts[dim] < queryCount+k {
		t.Fatalf("only %d vectors in the dominant dimension", counts[dim])
	}
	slices.SortFunc(saved.Representatives, func(a, b personRepresentative) int {
		if a.ID < b.ID {
			return -1
		}
		if a.ID > b.ID {
			return 1
		}
		return 0
	})
	f, err := os.Create(filepath.Join(t.TempDir(), "vectors.bin"))
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	writer := bufio.NewWriterSize(f, 256<<10)
	type query struct {
		id     int64
		vector vector
	}
	var queries []query
	record := make([]byte, 8+4*dim)
	number := 0
	for _, rep := range saved.Representatives {
		if len(rep.Vector) != dim {
			continue
		}
		v := normalizedPersonVector(rep.Vector)
		if number*queryCount/counts[dim] == len(queries) {
			queries = append(queries, query{rep.ID, v})
		}
		number++
		binary.LittleEndian.PutUint64(record, uint64(rep.ID))
		for i, value := range v {
			binary.LittleEndian.PutUint32(record[8+i*4:], math.Float32bits(value))
		}
		if _, err = writer.Write(record); err != nil {
			t.Fatal(err)
		}
	}
	if err = writer.Flush(); err != nil {
		t.Fatal(err)
	}
	saved = savedPersonHNSW{}
	graphHeap := heap()
	graphTimes := make([]time.Duration, 0, len(queries))
	graphHits := make([]map[int64]bool, len(queries))
	for i, q := range queries {
		start = time.Now()
		nodes := index.graphs[dim].Search(q.vector, k+1)
		graphTimes = append(graphTimes, time.Since(start))
		graphHits[i] = make(map[int64]bool)
		for _, node := range nodes {
			if node.Key != q.id && len(graphHits[i]) < k {
				graphHits[i][node.Key] = true
			}
		}
	}
	index = nil
	reader := bufio.NewReaderSize(f, 256<<10)
	diskHeap := heap()
	diskTimes := make([]time.Duration, 0, len(queries))
	overlap := 0
	for i, q := range queries {
		start = time.Now()
		if _, err = f.Seek(0, io.SeekStart); err != nil {
			t.Fatal(err)
		}
		reader.Reset(f)
		type hit struct {
			id    int64
			score float32
		}
		var best []hit
		for range number {
			if _, err = io.ReadFull(reader, record); err != nil {
				t.Fatal(err)
			}
			id := int64(binary.LittleEndian.Uint64(record))
			if id == q.id {
				continue
			}
			var score float32
			for j, value := range q.vector {
				score += value * math.Float32frombits(binary.LittleEndian.Uint32(record[8+j*4:]))
			}
			if len(best) == k && score <= best[k-1].score {
				continue
			}
			if len(best) < k {
				best = append(best, hit{})
			}
			j := len(best) - 1
			for j > 0 && score > best[j-1].score {
				best[j] = best[j-1]
				j--
			}
			best[j] = hit{id, score}
		}
		diskTimes = append(diskTimes, time.Since(start))
		for _, h := range best {
			if graphHits[i][h.id] {
				overlap++
			}
		}
	}
	slices.Sort(graphTimes)
	slices.Sort(diskTimes)
	t.Logf("%d vectors × %d dimensions, %d queries, top %d, disk file %.1f MiB", number, dim, len(queries), k, float64(number*len(record))/(1<<20))
	t.Logf("HNSW load %s, retained heap %.2f MiB, median %s, p95 %s", loadTime.Round(time.Millisecond), float64(int64(graphHeap)-int64(baseline))/(1<<20), graphTimes[len(queries)/2], graphTimes[(len(queries)*95+99)/100-1])
	t.Logf("Disk scan retained heap %.2f MiB, median %s, p95 %s, HNSW recall %.1f%%", float64(int64(diskHeap)-int64(baseline))/(1<<20), diskTimes[len(queries)/2], diskTimes[(len(queries)*95+99)/100-1], 100*float64(overlap)/float64(k*len(queries)))
}
