package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/gob"
	"fmt"
	"log"
	"math"
	"math/rand"
	"os"
	"path/filepath"
	"sort"
	"time"

	"github.com/amitbet/home-images/internal/hnsw"
)

// SQLite owns the representatives. This file is only a disposable search cache.
// The transactional revision also covers writes made outside the HTTP handlers.
func (a *AI) initPersonHNSW() error {
	_, err := a.db.Exec(`INSERT OR IGNORE INTO ai_settings VALUES('people_vector_revision','0');
INSERT OR IGNORE INTO ai_settings VALUES('people_vector_instance',lower(hex(randomblob(16))));
CREATE TRIGGER IF NOT EXISTS people_vector_insert AFTER INSERT ON people BEGIN
 UPDATE ai_settings SET value=CAST(value AS INTEGER)+1 WHERE key='people_vector_revision'; END;
CREATE TRIGGER IF NOT EXISTS people_vector_update AFTER UPDATE OF embedding,model,merged_into ON people BEGIN
 UPDATE ai_settings SET value=CAST(value AS INTEGER)+1 WHERE key='people_vector_revision'; END;
CREATE TRIGGER IF NOT EXISTS people_vector_delete AFTER DELETE ON people BEGIN
 UPDATE ai_settings SET value=CAST(value AS INTEGER)+1 WHERE key='people_vector_revision'; END;`)
	return err
}

type personRepresentative struct {
	ID, Group int64
	Vector    vector
}

type personHNSW struct {
	revision string
	vectors  map[int64][]vector
	groups   map[int64]int64
	graphs   map[int]*hnsw.Graph[int64] // isolate dimensions, including legacy models
	savedAt  time.Time
}

type savedPersonHNSW struct {
	Version               int
	Root, Model, Revision string
	Representatives       []personRepresentative
	Graphs                map[int][]byte
}

func validPersonVector(v vector) bool {
	if len(v) < 2 || len(v) > 8192 {
		return false
	}
	var norm float64
	for _, x := range v {
		if math.IsNaN(float64(x)) || math.IsInf(float64(x), 0) {
			return false
		}
		norm += float64(x) * float64(x)
	}
	return norm > 0
}

func newPersonGraph() *hnsw.Graph[int64] {
	g := hnsw.NewGraph[int64]()
	g.Distance = hnsw.UnitCosineDistance
	g.M = 16
	g.EfSearch = 128
	g.Rng = rand.New(rand.NewSource(1))
	return g
}

func normalizedPersonVector(v vector) vector {
	var norm float64
	for _, x := range v {
		norm += float64(x) * float64(x)
	}
	unit := make(vector, len(v))
	n := math.Sqrt(norm)
	for i, x := range v {
		unit[i] = float32(float64(x) / n)
	}
	return unit
}

// Called under personIndexMu. A read transaction pairs the revision with exactly
// the rows it describes; a concurrent commit will be picked up next request.
func (a *AI) loadPersonHNSW(ctx context.Context) (*personHNSW, error) {
	tx, err := a.read.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	var revision string
	if err = tx.QueryRowContext(ctx, "SELECT (SELECT value FROM ai_settings WHERE key='people_vector_instance')||':'||value FROM ai_settings WHERE key='people_vector_revision'").Scan(&revision); err != nil {
		return nil, err
	}
	if a.personIndex != nil && a.personIndex.revision == revision {
		return a.personIndex, nil
	}
	path := filepath.Join(a.lib.config.DataDir, "people.hnsw")
	var cached *savedPersonHNSW
	if a.personIndex == nil {
		if saved, e := readPersonHNSW(path); e == nil && saved.Version == 3 && saved.Model == a.lib.config.ML.FaceModel {
			cached = &saved
			if saved.Root == a.lib.rootPath && saved.Revision == revision {
				if index, e := restorePersonHNSW(saved); e == nil {
					a.personIndex = index
					return index, nil
				}
			}
		}
	}
	rows, err := tx.QueryContext(ctx, "SELECT id,coalesce(merged_into,id),embedding FROM people WHERE model=? ORDER BY id", a.lib.config.ML.FaceModel)
	if err != nil {
		return nil, err
	}
	saved := savedPersonHNSW{Version: 3, Root: a.lib.rootPath, Model: a.lib.config.ML.FaceModel, Revision: revision, Graphs: map[int][]byte{}}
	for rows.Next() {
		var p personRepresentative
		var blob []byte
		if err = rows.Scan(&p.ID, &p.Group, &blob); err != nil {
			rows.Close()
			return nil, err
		}
		p.Vector = vectorBlob(blob)
		if validPersonVector(p.Vector) {
			saved.Representatives = append(saved.Representatives, p)
		}
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return nil, err
	}
	if err = tx.Commit(); err != nil {
		return nil, err
	}
	// An offline-built cache or a stale merge mapping can reuse its topology,
	// but only after checking every representative against the current database.
	if cached != nil && samePersonVectors(cached.Representatives, saved.Representatives) {
		cached.Root, cached.Revision = saved.Root, saved.Revision
		cached.Representatives = saved.Representatives
		if index, e := restorePersonHNSW(*cached); e == nil {
			if e = writePersonHNSW(path, *cached); e != nil {
				log.Printf("people HNSW cache: %v", e)
			}
			a.personIndex = index
			return index, nil
		}
	}
	// Indexing can append representatives between the last cache write and a
	// restart. Restore that topology first; the reuse check below validates every
	// existing node before adding new vectors or rebuilding on replacements.
	if cached != nil && a.personIndex == nil {
		if old, e := restorePersonHNSW(*cached); e == nil {
			old.revision = ""
			a.personIndex = old
		}
	}
	index := &personHNSW{revision: revision, vectors: map[int64][]vector{}, groups: map[int64]int64{}, graphs: map[int]*hnsw.Graph[int64]{}}
	// Most writes only add representatives or change their merged group. Reuse
	// graph topology for those; deletion/replacement takes the safe rebuild path.
	old := a.personIndex
	if old != nil {
		present := make(map[int64]personRepresentative, len(saved.Representatives))
		for _, p := range saved.Representatives {
			present[p.ID] = p
		}
		reuse := true
		for id := range old.groups {
			p, ok := present[id]
			if !ok {
				reuse = false
				break
			}
			g := old.graphs[len(p.Vector)]
			if g == nil {
				reuse = false
				break
			}
			v, ok := g.Lookup(id)
			if !ok || !bytes.Equal(vector(v).blob(), normalizedPersonVector(p.Vector).blob()) {
				reuse = false
				break
			}
		}
		if reuse {
			index.graphs = old.graphs
			index.savedAt = old.savedAt
			// Track every reused node even if cancellation interrupts remapping.
			// A later deletion/replacement must still force a topology rebuild.
			for id, group := range old.groups {
				index.groups[id] = group
			}
		}
	}
	// Keep unfinished topology available if the browser cancels the first build.
	// Its empty revision prevents it being served as a complete search snapshot.
	index.revision = ""
	a.personIndex = index
	for _, p := range saved.Representatives {
		if err = ctx.Err(); err != nil {
			return nil, err
		}
		index.vectors[p.Group] = append(index.vectors[p.Group], p.Vector)
		index.groups[p.ID] = p.Group
		g := index.graphs[len(p.Vector)]
		if g == nil {
			g = newPersonGraph()
			index.graphs[len(p.Vector)] = g
		}
		if _, exists := g.Lookup(p.ID); !exists {
			g.Add(hnsw.MakeNode(p.ID, []float32(normalizedPersonVector(p.Vector))))
		}
	}
	if time.Since(index.savedAt) >= 30*time.Second {
		for dim, g := range index.graphs {
			var b bytes.Buffer
			if err = g.Export(&b); err != nil {
				return nil, err
			}
			saved.Graphs[dim] = b.Bytes()
		}
		if err = ctx.Err(); err != nil {
			return nil, err
		}
		if err = writePersonHNSW(path, saved); err != nil {
			// Read-only/full cache storage must not disable people search.
			log.Printf("people HNSW cache: %v", err)
		} else {
			index.savedAt = time.Now()
		}
	}
	index.revision = revision
	a.personIndex = index
	return index, nil
}

func samePersonVectors(a, b []personRepresentative) bool {
	if len(a) != len(b) {
		return false
	}
	for i, p := range a {
		if p.ID != b[i].ID || !bytes.Equal(p.Vector.blob(), b[i].Vector.blob()) {
			return false
		}
	}
	return true
}

func writePersonHNSW(path string, saved savedPersonHNSW) error {
	var payload bytes.Buffer
	if err := gob.NewEncoder(&payload).Encode(saved); err != nil {
		return err
	}
	digest := sha256.Sum256(payload.Bytes())
	f, err := os.CreateTemp(filepath.Dir(path), ".people-hnsw-*")
	if err != nil {
		return err
	}
	defer os.Remove(f.Name())
	defer f.Close()
	if _, err = f.Write(digest[:]); err != nil {
		return err
	}
	if _, err = f.Write(payload.Bytes()); err != nil {
		return err
	}
	if err = f.Sync(); err != nil {
		return err
	}
	if err = f.Close(); err != nil {
		return err
	}
	return os.Rename(f.Name(), path)
}

func readPersonHNSW(path string) (saved savedPersonHNSW, err error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return saved, err
	}
	if len(b) < sha256.Size {
		return saved, fmt.Errorf("truncated HNSW cache")
	}
	digest := sha256.Sum256(b[sha256.Size:])
	if !bytes.Equal(b[:sha256.Size], digest[:]) {
		return saved, fmt.Errorf("HNSW cache checksum mismatch")
	}
	err = gob.NewDecoder(bytes.NewReader(b[sha256.Size:])).Decode(&saved)
	return saved, err
}

func restorePersonHNSW(saved savedPersonHNSW) (index *personHNSW, err error) {
	// A corrupt optional cache must never bring down the server.
	defer func() {
		if r := recover(); r != nil {
			index = nil
			err = fmt.Errorf("invalid HNSW cache: %v", r)
		}
	}()
	index = &personHNSW{revision: saved.Revision, vectors: map[int64][]vector{}, groups: map[int64]int64{}, graphs: map[int]*hnsw.Graph[int64]{}, savedAt: time.Now()}
	for dim, blob := range saved.Graphs {
		g := newPersonGraph()
		if err = g.Import(bytes.NewReader(blob)); err != nil {
			return nil, err
		}
		index.graphs[dim] = g
	}
	count := 0
	for _, g := range index.graphs {
		count += g.Len()
	}
	if count != len(saved.Representatives) {
		return nil, fmt.Errorf("HNSW representative count mismatch")
	}
	for _, p := range saved.Representatives {
		g := index.graphs[len(p.Vector)]
		if !validPersonVector(p.Vector) || g == nil {
			return nil, fmt.Errorf("invalid HNSW representative")
		}
		v, ok := g.Lookup(p.ID)
		if !ok || !bytes.Equal(vector(v).blob(), normalizedPersonVector(p.Vector).blob()) {
			return nil, fmt.Errorf("HNSW vector mismatch")
		}
		index.vectors[p.Group] = append(index.vectors[p.Group], p.Vector)
		index.groups[p.ID] = p.Group
	}
	return index, nil
}

// Existing representatives already have graph nodes. For very large merged
// groups, expand their shared neighborhoods instead of running thousands of
// nearly identical vector searches. Every retained source node participates.
func (index *personHNSW) groupNeighborhood(ctx context.Context, source int64, depth int) (map[int64]float32, error) {
	scores := map[int64]float32{}
	for id, group := range index.groups {
		if group != source {
			continue
		}
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		for _, g := range index.graphs {
			query, ok := g.Lookup(id)
			if !ok {
				continue
			}
			visited := map[int64]bool{id: true}
			frontier := []int64{id}
			for hop := 0; hop < depth; hop++ {
				var next []int64
				for _, key := range frontier {
					for _, n := range g.Neighbors(key) {
						if visited[n.Key] {
							continue
						}
						visited[n.Key] = true
						next = append(next, n.Key)
						other := index.groups[n.Key]
						if other == source {
							continue
						}
						score := similarity(query, n.Value)
						if old, ok := scores[other]; !ok || score > old {
							scores[other] = score
						}
					}
				}
				frontier = next
			}
		}
	}
	return scores, nil
}

// Exact reranking uses every source representative after graph candidate lookup.
func (index *personHNSW) candidates(ctx context.Context, source int64, k int) (map[int64]bool, error) {
	groups := map[int64]bool{}
	scores := map[int64]float32{}
	if k >= len(index.groups) {
		for group := range index.vectors {
			if group != source {
				groups[group] = true
			}
		}
		return groups, nil
	}
	if len(index.vectors[source]) > 64 {
		depth := 2
		if k > 4096 {
			depth = 3
		}
		var err error
		scores, err = index.groupNeighborhood(ctx, source, depth)
		if err != nil {
			return nil, err
		}
		for group := range scores {
			groups[group] = true
		}
	} else {
		for _, v := range index.vectors[source] {
			if err := ctx.Err(); err != nil {
				return nil, err
			}
			g := index.graphs[len(v)]
			if k >= g.Len() {
				// Exhaustive fallback also preserves complete paging for small graphs.
				for id := range index.vectors {
					groups[id] = true
				}
				continue
			}
			query := normalizedPersonVector(v)
			for _, n := range g.Search(query, k) {
				group := index.groups[n.Key]
				groups[group] = true
				score := similarity(query, n.Value)
				if old, ok := scores[group]; !ok || score > old {
					scores[group] = score
				}
			}
		}
	}
	delete(groups, source)
	if k < len(index.groups) && len(groups) > k {
		ids := sortedPersonIDs(groups)
		sort.Slice(ids, func(i, j int) bool {
			if scores[ids[i]] == scores[ids[j]] {
				return ids[i] < ids[j]
			}
			return scores[ids[i]] > scores[ids[j]]
		})
		for _, id := range ids[k:] {
			delete(groups, id)
		}
	}
	return groups, nil
}

func sortedPersonIDs(groups map[int64]bool) []int64 {
	ids := make([]int64, 0, len(groups))
	for id := range groups {
		ids = append(ids, id)
	}
	sort.Slice(ids, func(i, j int) bool { return ids[i] < ids[j] })
	return ids
}
