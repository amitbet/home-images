package main

import (
	"bufio"
	"context"
	"database/sql"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"math"
	"os"
	"path/filepath"
)

type peopleDiskHeader struct {
	Version         int
	Revision, Model string
}

const peopleDiskBuffer = 256 << 10

// The SQLite revision and file contents must describe the same read snapshot.
func (a *AI) openPeopleDiskIndex(ctx context.Context, tx *sql.Tx) (*os.File, *bufio.Reader, error) {
	header := peopleDiskHeader{Version: 1, Model: a.lib.config.ML.FaceModel}
	if err := tx.QueryRowContext(ctx, "SELECT (SELECT value FROM ai_settings WHERE key='people_vector_instance')||':'||value FROM ai_settings WHERE key='people_vector_revision'").Scan(&header.Revision); err != nil {
		return nil, nil, err
	}
	path := filepath.Join(a.lib.config.DataDir, "people-vectors.bin")
	if f, err := os.Open(path); err == nil {
		reader := bufio.NewReaderSize(f, peopleDiskBuffer)
		var size uint64
		encoded, readErr := reader.ReadSlice('\n')
		var stored peopleDiskHeader
		if readErr == nil && json.Unmarshal(encoded, &stored) == nil && stored == header &&
			binary.Read(reader, binary.LittleEndian, &size) == nil {
			if stat, statErr := f.Stat(); statErr == nil && uint64(stat.Size()) == size {
				return f, reader, nil
			}
		}
		f.Close()
	}
	if err := writePeopleDiskIndex(ctx, tx, path, header); err != nil {
		return nil, nil, err
	}
	f, err := os.Open(path)
	if err != nil {
		return nil, nil, err
	}
	reader := bufio.NewReaderSize(f, peopleDiskBuffer)
	if _, err = reader.ReadSlice('\n'); err == nil {
		_, err = reader.Discard(8)
	}
	if err != nil {
		f.Close()
		return nil, nil, err
	}
	return f, reader, nil
}

func writePeopleDiskIndex(ctx context.Context, tx *sql.Tx, path string, header peopleDiskHeader) error {
	file, err := os.CreateTemp(filepath.Dir(path), ".people-vectors-*")
	if err != nil {
		return err
	}
	defer os.Remove(file.Name())
	defer file.Close()

	headerBytes, err := json.Marshal(header)
	if err != nil {
		return err
	}
	headerBytes = append(headerBytes, '\n')
	writer := bufio.NewWriterSize(file, peopleDiskBuffer)
	if _, err = writer.Write(headerBytes); err != nil {
		return err
	}
	if _, err = writer.Write(make([]byte, 8)); err != nil {
		return err
	}
	rows, err := tx.QueryContext(ctx, "SELECT coalesce(merged_into,id),embedding FROM people WHERE model=? ORDER BY id", header.Model)
	if err != nil {
		return err
	}
	defer rows.Close()
	var record [10]byte
	for rows.Next() {
		var group int64
		var blob sql.RawBytes
		if err = rows.Scan(&group, &blob); err != nil {
			return err
		}
		if len(blob)%4 != 0 || !validPersonVector(vectorBlob(blob)) {
			continue
		}
		binary.LittleEndian.PutUint64(record[:8], uint64(group))
		binary.LittleEndian.PutUint16(record[8:], uint16(len(blob)/4))
		if _, err = writer.Write(record[:]); err != nil {
			return err
		}
		if _, err = writer.Write(blob); err != nil {
			return err
		}
	}
	if err = rows.Err(); err != nil {
		return err
	}
	if err = writer.Flush(); err != nil {
		return err
	}
	size, err := file.Seek(0, io.SeekCurrent)
	if err != nil {
		return err
	}
	var encodedSize [8]byte
	binary.LittleEndian.PutUint64(encodedSize[:], uint64(size))
	if _, err = file.WriteAt(encodedSize[:], int64(len(headerBytes))); err != nil {
		return err
	}
	if err = file.Close(); err != nil {
		return err
	}
	if err = ctx.Err(); err != nil {
		return err
	}
	return os.Rename(file.Name(), path)
}

func (a *AI) warmPeopleDiskIndex() {
	a.refreshWG.Add(1)
	go func() {
		defer a.refreshWG.Done()
		ctx := a.refreshCtx
		a.personIndexMu.Lock()
		defer a.personIndexMu.Unlock()
		tx, err := a.read.BeginTx(ctx, nil)
		if err == nil {
			defer tx.Rollback()
			var file *os.File
			file, _, err = a.openPeopleDiskIndex(ctx, tx)
			if err == nil {
				err = file.Close()
			}
		}
		if err != nil && ctx.Err() == nil {
			log.Printf("People disk index warmup: %v", err)
		}
	}()
}

func peopleDiskSource(ctx context.Context, tx *sql.Tx, model string, id int64) (map[int][]vector, error) {
	rows, err := tx.QueryContext(ctx, `SELECT embedding FROM (
		SELECT id, embedding FROM people WHERE model=? AND id=? AND merged_into IS NULL
		UNION ALL SELECT id, embedding FROM people WHERE model=? AND merged_into=?
	) ORDER BY id`, model, id, model, id)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	vectors := make(map[int][]vector)
	for rows.Next() {
		var blob sql.RawBytes
		if err = rows.Scan(&blob); err != nil {
			return nil, err
		}
		v := vectorBlob(blob)
		if len(blob)%4 == 0 && validPersonVector(v) {
			vectors[len(v)] = append(vectors[len(v)], v)
		}
	}
	return vectors, rows.Err()
}

// Large merged groups can have thousands of representatives. The interactive
// request samples across each group; exact review can score every pair.
func samplePeopleDiskSource(source map[int][]vector) bool {
	const limit = 4
	sampled := false
	for dimension, vectors := range source {
		if len(vectors) <= limit {
			continue
		}
		reps := make([]vector, limit)
		for i := range reps {
			reps[i] = vectors[i*(len(vectors)-1)/(limit-1)]
		}
		source[dimension] = reps
		sampled = true
	}
	return sampled
}

func scorePeopleDiskIndex(ctx context.Context, reader *bufio.Reader, source map[int][]vector, id int64, minimum float64) (map[int64]float32, error) {
	scores := make(map[int64]float32)
	var record [10]byte
	blob := make([]byte, 8192*4)
	embedding := make(vector, 8192)
	for {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		if _, err := io.ReadFull(reader, record[:]); err != nil {
			if errors.Is(err, io.EOF) {
				return scores, nil
			}
			return nil, err
		}
		group := int64(binary.LittleEndian.Uint64(record[:8]))
		count := int(binary.LittleEndian.Uint16(record[8:]))
		if count < 2 || count > len(embedding) {
			return nil, fmt.Errorf("invalid vector dimension in People disk index: %d", count)
		}
		data := blob[:4*count]
		if _, err := io.ReadFull(reader, data); err != nil {
			return nil, err
		}
		if group == id {
			continue
		}
		target := embedding[:count]
		if queries := source[count]; len(queries) != 0 {
			for i := range target {
				target[i] = math.Float32frombits(binary.LittleEndian.Uint32(data[4*i:]))
			}
			score := scores[group]
			for _, query := range queries {
				score = max(score, similarity(query, target))
			}
			if float64(score) >= minimum {
				scores[group] = score
			}
		} else if minimum == 0 {
			scores[group] = max(scores[group], 0)
		}
	}
}
