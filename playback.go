package main

import (
	"container/heap"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"math/rand/v2"
	"net/http"
	"runtime"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"
)

const playbackTTL = 2 * time.Minute

type playSelection struct {
	Generation string
	Mode       string
	Hits       []ranked
	Total      int
	Created    time.Time
}

// Saved selections are immutable rules for this request; explicit query keys,
// including empty values, override them. Never resolve another root's collection.
func (a *AI) playbackRequest(r *http.Request) (*http.Request, error) {
	request := r.Clone(r.Context())
	u := *r.URL
	request.URL = &u
	query := u.Query()
	if value := query.Get("collection"); value != "" {
		id, err := strconv.ParseInt(value, 10, 64)
		if err != nil || id < 1 {
			return nil, bad(400, "Invalid collection")
		}
		var raw string
		err = a.read.QueryRowContext(r.Context(), "SELECT query FROM curation.collections WHERE id=? AND root=?", id, a.lib.rootPath).Scan(&raw)
		if errors.Is(err, sql.ErrNoRows) {
			return nil, bad(404, "Saved selection not found")
		}
		if err != nil {
			return nil, err
		}
		var saved map[string]string
		if err = json.Unmarshal([]byte(raw), &saved); err != nil {
			return nil, err
		}
		for key, value := range saved {
			if !query.Has(key) {
				query.Set(key, value)
			}
		}
	}
	u.RawQuery = query.Encode()
	return request, nil
}
func (a *AI) playbackFilter(r *http.Request, mode string) (string, []any, error) {
	request := r.Clone(r.Context())
	u := *r.URL
	request.URL = &u
	if mode == "semantic" {
		q := u.Query()
		q.Del("q")
		u.RawQuery = q.Encode()
	}
	clause, args, err := a.lib.selection(request)
	if err != nil {
		return "", nil, err
	}
	if person := r.URL.Query().Get("person"); person != "" {
		id, e := strconv.ParseInt(person, 10, 64)
		if e != nil || id < 1 {
			return "", nil, bad(400, "Invalid person")
		}
		clause += ` AND EXISTS(SELECT 1 FROM faces f JOIN ai_images ai ON ai.image_id=f.image_id WHERE f.image_id=i.id AND f.person_id=? AND ai.signature=i.signature AND ai.models=? AND ai.error='')`
		args = append(args, id, a.models)
	}
	return clause, args, nil
}
func (a *AI) rankSelection(ctx context.Context, q, clause string, args []any, limit int) (playSelection, error) {
	result := playSelection{Created: time.Now(), Mode: "semantic"}
	if a.lib.config.ML.URL == "" {
		return result, bad(503, "Configure the Mac ML worker to enable content search")
	}
	select {
	case a.searchSlot <- struct{}{}:
		defer func() { <-a.searchSlot }()
	default:
		return result, bad(429, "Another content search is running; try again shortly")
	}
	v, err := a.textVector(ctx, q)
	if err != nil {
		return result, err
	}
	rows, err := a.read.QueryContext(ctx, `SELECT i.id,a.clip FROM catalog.images i JOIN ai_images a ON a.image_id=i.id AND a.signature=i.signature WHERE a.models=? AND a.error='' AND `+clause, append([]any{a.models}, args...)...)
	if err != nil {
		return result, err
	}
	defer rows.Close()
	top := &ranking{}
	heap.Init(top)
	for rows.Next() {
		var id string
		var blob []byte
		if err = rows.Scan(&id, &blob); err != nil {
			return result, err
		}
		if len(blob) != len(v)*4 {
			continue
		}
		result.Total++
		score := similarity(v, vectorBlob(blob))
		if top.Len() < limit {
			heap.Push(top, ranked{id, score})
		} else if score > (*top)[0].Score || (score == (*top)[0].Score && id < (*top)[0].ID) {
			heap.Pop(top)
			heap.Push(top, ranked{id, score})
		}
		if result.Total%256 == 0 {
			runtime.Gosched()
			if ctx.Err() != nil {
				return result, ctx.Err()
			}
		}
	}
	if err = rows.Err(); err != nil {
		return result, err
	}
	result.Hits = make([]ranked, top.Len())
	for i := len(result.Hits) - 1; i >= 0; i-- {
		result.Hits[i] = heap.Pop(top).(ranked)
	}
	return result, nil
}
func (a *AI) cachedSelection(r *http.Request, q, mode, clause string, args []any, limit int) (playSelection, error) {
	filters := r.URL.Query()
	for _, key := range []string{"after", "order", "refresh", "collection", "offset", "limit"} {
		filters.Del(key)
	}
	filters.Set("pool_limit", strconv.Itoa(limit))
	filters.Set("mode", mode)
	filters.Set("q", q)
	generation := "initial"
	if scan := a.lib.Status().LastScan; scan != nil {
		generation = *scan
	}
	key := fmt.Sprintf("%s|%s|%d|%s", a.lib.rootPath, a.models, a.selectionRevision.Load(), filters.Encode())
	a.playMu.Lock()
	cached, ok := a.playCache[key]
	if ok && r.URL.Query().Get("refresh") != "1" {
		// Keep frame requests fast while an old snapshot is refreshed. Every
		// returned ID is still checked against current filters and file state.
		if (time.Since(cached.Created) >= playbackTTL || cached.Generation != generation) && !a.playRefreshing {
			a.playRefreshing = true
			a.refreshWG.Add(1)
			go func() {
				defer a.refreshWG.Done()
				ctx, cancel := context.WithTimeout(a.refreshCtx, 90*time.Second)
				defer cancel()
				request := r.Clone(ctx)
				var next playSelection
				var err error
				if mode == "semantic" {
					next, err = a.rankSelection(ctx, q, clause, args, limit)
				} else {
					next, err = a.metadataSelection(request, clause, args)
				}
				a.playMu.Lock()
				a.playRefreshing = false
				if _, exists := a.playCache[key]; err == nil && exists {
					next.Generation = generation
					a.playCache[key] = next
				}
				a.playMu.Unlock()
			}()
		}
		a.playMu.Unlock()
		return cached, nil
	}
	a.playMu.Unlock()
	var selection playSelection
	var err error
	if mode == "semantic" {
		selection, err = a.rankSelection(r.Context(), q, clause, args, limit)
	} else {
		selection, err = a.metadataSelection(r, clause, args)
	}
	if err != nil {
		return selection, err
	}
	selection.Generation = generation
	a.playMu.Lock()
	if a.playCache == nil {
		a.playCache = map[string]playSelection{}
	}
	if len(a.playCache) >= 8 {
		var oldest string
		var age time.Time
		for k, v := range a.playCache {
			if oldest == "" || v.Created.Before(age) {
				oldest, age = k, v.Created
			}
		}
		delete(a.playCache, oldest)
	}
	a.playCache[key] = selection
	a.playMu.Unlock()
	return selection, nil
}
func (a *AI) playbackNext(w http.ResponseWriter, r *http.Request) error {
	r, err := a.playbackRequest(r)
	if err != nil {
		return err
	}
	query := r.URL.Query()
	order := query.Get("order")
	if order == "" {
		order = "random"
	}
	if order != "random" && order != "sequential" {
		return bad(400, "order must be random or sequential")
	}
	mode := query.Get("mode")
	if mode == "" {
		mode = "semantic"
	}
	if mode != "semantic" && mode != "metadata" {
		return bad(400, "mode must be semantic or metadata")
	}
	q := strings.TrimSpace(query.Get("q"))
	if utf8.RuneCountInString(q) > 500 {
		return bad(400, "Query too long")
	}
	if q == "" {
		mode = "metadata"
	}
	clause, args, err := a.playbackFilter(r, mode)
	if err != nil {
		return err
	}
	after := query.Get("after")
	limit := 0
	if mode == "semantic" {
		limit, err = intParam(r, "pool_limit", 500, 1, 2000)
		if err != nil {
			return err
		}
	}
	selection, err := a.cachedSelection(r, q, mode, clause, args, limit)
	if err != nil {
		return err
	}
	n := len(selection.Hits)
	if n == 0 {
		return bad(404, "No photos match this selection; content searches cover indexed photos only")
	}
	start := 0
	if order != "random" {
		for i, hit := range selection.Hits {
			if hit.ID == after {
				start = (i + 1) % n
				break
			}
		}
	}
	indices := make([]int, n)
	if order == "random" {
		indices = rand.Perm(n)
	} else {
		for i := range indices {
			indices[i] = (start + i) % n
		}
	}
	// Recheck current signatures, filters, and file existence even for cached IDs.
	// The cursor is the image ID, so clients never share mutable playback state.
	var fallback *ranked
	for _, index := range indices {
		hit := selection.Hits[index]
		if order == "random" && hit.ID == after {
			h := hit
			fallback = &h
			continue
		}
		if p, ok, err := a.playbackPhoto(r, hit, mode, clause, args); err != nil {
			return err
		} else if ok {
			return writePlayback(w, p, playbackInfo(order, selection, limit), playbackScore(mode, hit))
		}
	}
	if fallback != nil {
		if p, ok, err := a.playbackPhoto(r, *fallback, mode, clause, args); err != nil {
			return err
		} else if ok {
			return writePlayback(w, p, playbackInfo(order, selection, limit), playbackScore(mode, *fallback))
		}
	}
	return bad(404, "No current photos remain in this selection; retry with refresh=1")
}
func (a *AI) playbackPhoto(r *http.Request, hit ranked, mode, clause string, args []any) (Photo, bool, error) {
	var valid int
	query := "SELECT count(*) FROM catalog.images i WHERE i.id=? AND " + clause
	parameters := append([]any{hit.ID}, args...)
	if mode == "semantic" {
		query = `SELECT count(*) FROM catalog.images i JOIN ai_images a ON a.image_id=i.id AND a.signature=i.signature WHERE i.id=? AND a.models=? AND a.error='' AND ` + clause
		parameters = append([]any{hit.ID, a.models}, args...)
	}
	err := a.read.QueryRowContext(r.Context(), query, parameters...).Scan(&valid)
	if err != nil {
		return Photo{}, false, err
	}
	if valid == 0 {
		return Photo{}, false, nil
	}
	p, err := a.lib.get(r.Context(), hit.ID)
	if err != nil {
		var api *apiError
		if errors.As(err, &api) && (api.code == 404 || api.code == 403) {
			return Photo{}, false, nil
		}
		return p, false, err
	}
	return p, true, nil
}
func playbackInfo(order string, selection playSelection, limit int) map[string]any {
	info := map[string]any{"mode": selection.Mode, "order": order, "pool_size": len(selection.Hits), "cached_at": selection.Created.UTC().Format(time.RFC3339Nano), "cache_seconds": int(playbackTTL.Seconds())}
	if selection.Mode == "semantic" {
		info["pool_limit"] = limit
		info["indexed_candidates"] = selection.Total
	} else {
		info["selection_size"] = selection.Total
	}
	return info
}
func playbackScore(mode string, hit ranked) *float32 {
	if mode != "semantic" {
		return nil
	}
	return &hit.Score
}
func (a *AI) metadataSelection(r *http.Request, clause string, args []any) (playSelection, error) {
	result := playSelection{Created: time.Now(), Mode: "metadata"}
	// For text searches, favor a sequential table scan and sort only matching IDs.
	// Broad browsing uses the compact covering path index without reading EXIF JSON.
	order := "path COLLATE NOCASE,path"
	if strings.TrimSpace(r.URL.Query().Get("q")) != "" {
		order = "+path COLLATE NOCASE,+path"
	}
	rows, err := a.read.QueryContext(r.Context(), "SELECT i.id FROM catalog.images i WHERE "+clause+" ORDER BY "+order, args...)
	if err != nil {
		return result, err
	}
	defer rows.Close()
	for rows.Next() {
		var id string
		if err = rows.Scan(&id); err != nil {
			return result, err
		}
		result.Hits = append(result.Hits, ranked{ID: id})
	}
	result.Total = len(result.Hits)
	return result, rows.Err()
}
func writePlayback(w http.ResponseWriter, p Photo, info map[string]any, score *float32) error {
	raw, err := json.Marshal(p)
	if err != nil {
		return err
	}
	var item map[string]any
	if err = json.Unmarshal(raw, &item); err != nil {
		return err
	}
	item["playback"] = info
	if score != nil {
		item["similarity"] = *score
	}
	return writeJSON(w, 200, item)
}
