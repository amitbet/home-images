package main

import (
	"bytes"
	"context"
	"database/sql"
	"log"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"

	"github.com/amitbet/home-images/internal/hnsw"
)

// Read only rows changed since the in-memory snapshot. The journal is committed
// with the people write; gaps, deletions and replaced vectors use the full path.
func (a *AI) updatePersonHNSW(ctx context.Context, tx *sql.Tx, revision string) (bool, error) {
	index := a.personIndex
	instance, raw, _ := strings.Cut(index.revision, ":")
	current, nextRaw, _ := strings.Cut(revision, ":")
	before, e := strconv.ParseInt(raw, 10, 64)
	if e != nil {
		return false, nil
	}
	after, e := strconv.ParseInt(nextRaw, 10, 64)
	if e != nil || instance != current || after <= before || after-before > 100000 {
		return false, nil
	}
	var enabled int
	if e = tx.QueryRowContext(ctx, "SELECT count(*) FROM ai_settings WHERE key='people_vector_journal'").Scan(&enabled); e != nil {
		return false, e
	}
	if enabled == 0 {
		return false, nil
	}
	rows, e := tx.QueryContext(ctx, `SELECT c.person_id,p.model,p.embedding,coalesce(p.merged_into,p.id) FROM people_vector_changes c LEFT JOIN people p ON p.id=c.person_id WHERE c.revision>? AND c.revision<=? ORDER BY c.revision`, before, after)
	if e != nil {
		return false, e
	}
	changes := map[int64]personRepresentative{}
	count := int64(0)
	for rows.Next() {
		var id int64
		var model sql.NullString
		var group sql.NullInt64
		var blob []byte
		if e = rows.Scan(&id, &model, &blob, &group); e != nil {
			rows.Close()
			return false, e
		}
		count++
		p := personRepresentative{ID: id, Group: group.Int64, Vector: vectorBlob(blob)}
		valid := model.String == a.lib.config.ML.FaceModel && group.Valid && validPersonVector(p.Vector)
		old, exists := index.representatives[id]
		if exists && (!valid || !bytes.Equal(old.Vector.blob(), blob)) {
			rows.Close()
			return false, nil
		}
		if valid {
			changes[id] = p
		}
	}
	e = rows.Err()
	rows.Close()
	if e != nil {
		return false, e
	}
	if count != after-before {
		return false, nil
	}
	if e = ctx.Err(); e != nil {
		return false, e
	}
	index.revision = "" // cancellation cannot publish half-applied group mappings
	touched := map[int64]bool{}
	for id, p := range changes {
		if e = ctx.Err(); e != nil {
			return false, e
		}
		old, exists := index.representatives[id]
		if exists {
			touched[old.Group] = true
		} else {
			g := index.graphs[len(p.Vector)]
			if g == nil {
				g = newPersonGraph()
				index.graphs[len(p.Vector)] = g
			}
			g.Add(hnsw.MakeNode(id, []float32(normalizedPersonVector(p.Vector))))
		}
		index.representatives[id] = p
		index.groups[id] = p.Group
		touched[p.Group] = true
	}
	for group := range touched {
		delete(index.vectors, group)
	}
	ids := make([]int64, 0, len(index.representatives))
	for id := range index.representatives {
		ids = append(ids, id)
	}
	sort.Slice(ids, func(i, j int) bool { return ids[i] < ids[j] })
	for _, id := range ids {
		p := index.representatives[id]
		if touched[p.Group] {
			index.vectors[p.Group] = append(index.vectors[p.Group], p.Vector)
		}
	}
	index.revision = revision
	return true, nil
}

// Persist incremental updates on shutdown, outside the interactive search path.
// A stale disk snapshot remains safe: startup validates and adds missing nodes.
func (a *AI) flushPersonHNSW() {
	a.personIndexMu.Lock()
	defer a.personIndexMu.Unlock()
	index := a.personIndex
	if index == nil || index.revision == "" || index.revision == index.persistedRevision {
		return
	}
	saved := savedPersonHNSW{Version: 3, Root: a.lib.rootPath, Model: a.lib.config.ML.FaceModel, Revision: index.revision, Graphs: map[int][]byte{}}
	ids := make([]int64, 0, len(index.representatives))
	for id := range index.representatives {
		ids = append(ids, id)
	}
	sort.Slice(ids, func(i, j int) bool { return ids[i] < ids[j] })
	for _, id := range ids {
		saved.Representatives = append(saved.Representatives, index.representatives[id])
	}
	for dim, g := range index.graphs {
		var b bytes.Buffer
		if e := g.Export(&b); e != nil {
			log.Printf("people HNSW cache: %v", e)
			return
		}
		saved.Graphs[dim] = b.Bytes()
	}
	if e := writePersonHNSW(filepath.Join(a.lib.config.DataDir, "people.hnsw"), saved); e != nil {
		log.Printf("people HNSW cache: %v", e)
	} else {
		index.persistedRevision = index.revision
	}
}

func (a *AI) warmPersonHNSW() {
	if _, e := os.Stat(filepath.Join(a.lib.config.DataDir, "people.hnsw")); e != nil {
		return
	}
	a.refreshWG.Add(1)
	go func() {
		defer a.refreshWG.Done()
		a.personIndexMu.Lock()
		defer a.personIndexMu.Unlock()
		if a.personIndex == nil {
			if _, e := a.loadPersonHNSW(a.refreshCtx); e != nil && a.refreshCtx.Err() == nil {
				log.Printf("people HNSW warmup: %v", e)
			}
		}
	}()
}
