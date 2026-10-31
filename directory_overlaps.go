package main

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"path"
	"sort"
	"strings"
	"time"
)

type overlapFile struct {
	ID    string `json:"id"`
	Path  string `json:"path"`
	Hash  string `json:"hash"`
	Asset string `json:"asset"`
	Size  int64  `json:"size"`
}
type directoryOverlap struct {
	Source       string `json:"source"`
	Keep         string `json:"keep"`
	SourceFiles  int    `json:"source_files"`
	KeepFiles    int    `json:"keep_files"`
	ExactFiles   int    `json:"exact_files"`
	ContentFiles int    `json:"content_files"`
	UniqueFiles  int    `json:"unique_files"`
	PendingFiles int    `json:"pending_files"`
	Bytes        int64  `json:"bytes"`
	Full         bool   `json:"full"`
}

func directoryContains(dir, file string) bool { return dir == "" || strings.HasPrefix(file, dir+"/") }
func cleanOverlapDir(dir string) (string, error) {
	dir = strings.Trim(dir, "/")
	if dir == "" {
		return "", nil
	}
	name, err := cleanRelative(dir)
	if err != nil {
		return "", err
	}
	dir = strings.ReplaceAll(name, `\`, "/")
	if dir == "." {
		return "", nil
	}
	if strings.HasPrefix(dir, ".homeimages-quarantine") {
		return "", bad(400, "Trash cannot be selected")
	}
	return dir, nil
}
func disjointDirectories(a, b string) bool {
	return a != "" && b != "" && a != b && !directoryContains(a, b) && !directoryContains(b, a)
}
func (l *Library) overlapInventory(ctx context.Context, scope string, additionalScopes ...string) ([]overlapFile, error) {
	where := "1=1"
	args := []any{}
	if scope != "" {
		where = ""
		for _, dir := range append([]string{scope}, additionalScopes...) {
			if where != "" {
				where += " OR "
			}
			where += "(path>=? COLLATE BINARY AND path<? COLLATE BINARY)"
			args = append(args, dir+"/", dir+"0")
		}
	}
	rows, err := l.read.QueryContext(ctx, `SELECT id,path,size_bytes,CASE WHEN hash_signature=signature THEN coalesce(file_hash,'') ELSE '' END,CASE WHEN hash_signature=signature THEN coalesce(content_id,file_hash,'') ELSE '' END FROM images WHERE `+where, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	files := []overlapFile{}
	for rows.Next() {
		var f overlapFile
		if err = rows.Scan(&f.ID, &f.Path, &f.Size, &f.Hash, &f.Asset); err != nil {
			return nil, err
		}
		files = append(files, f)
	}
	return files, rows.Err()
}
func analyzeDirectoryOverlaps(ctx context.Context, files []overlapFile, scope string, depth int) ([]directoryOverlap, int, error) {
	type bucket struct {
		files   []overlapFile
		hashes  map[string]bool
		assets  map[string]bool
		pending int
	}
	buckets := map[string]*bucket{}
	for _, f := range files {
		relative := strings.TrimPrefix(f.Path, scope+"/")
		if scope == "" {
			relative = f.Path
		}
		parts := strings.Split(relative, "/")
		if len(parts) <= depth {
			continue
		}
		dir := strings.Join(parts[:depth], "/")
		if scope != "" {
			dir = scope + "/" + dir
		}
		b := buckets[dir]
		if b == nil {
			b = &bucket{hashes: map[string]bool{}, assets: map[string]bool{}}
			buckets[dir] = b
		}
		b.files = append(b.files, f)
		if f.Hash != "" {
			b.hashes[f.Hash] = true
		} else {
			b.pending++
		}
		if f.Asset != "" {
			b.assets[f.Asset] = true
		}
	}
	// Inverted lookup only compares trees that actually share some content.
	owners := map[string][]string{}
	for dir, b := range buckets {
		for asset := range b.assets {
			owners["content:"+asset] = append(owners["content:"+asset], dir)
		}
		for hash := range b.hashes {
			owners["file:"+hash] = append(owners["file:"+hash], dir)
		}
	}
	pairs := map[[2]string]bool{}
	for _, dirs := range owners {
		if err := ctx.Err(); err != nil {
			return nil, 0, err
		}
		for _, a := range dirs {
			for _, b := range dirs {
				if a != b {
					pairs[[2]string{a, b}] = true
				}
			}
		}
	}
	out := []directoryOverlap{}
	for pair := range pairs {
		if err := ctx.Err(); err != nil {
			return nil, 0, err
		}
		a, b := buckets[pair[0]], buckets[pair[1]]
		row := directoryOverlap{Source: pair[0], Keep: pair[1], SourceFiles: len(a.files), KeepFiles: len(b.files), PendingFiles: a.pending}
		for _, f := range a.files {
			exact := f.Hash != "" && b.hashes[f.Hash]
			if exact || (f.Asset != "" && b.assets[f.Asset]) {
				row.ContentFiles++
			}
			if exact {
				row.ExactFiles++
				row.Bytes += f.Size
			}
		}
		row.UniqueFiles = row.SourceFiles - row.ExactFiles
		row.Full = row.ExactFiles == row.SourceFiles
		out = append(out, row)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Bytes != out[j].Bytes {
			return out[i].Bytes > out[j].Bytes
		}
		if out[i].ExactFiles != out[j].ExactFiles {
			return out[i].ExactFiles > out[j].ExactFiles
		}
		if out[i].Source != out[j].Source {
			return out[i].Source < out[j].Source
		}
		return out[i].Keep < out[j].Keep
	})
	return out, len(buckets), nil
}
func (l *Library) directoryOverlaps(w http.ResponseWriter, r *http.Request) error {
	scope, err := cleanOverlapDir(r.URL.Query().Get("scope"))
	if err != nil {
		return err
	}
	depth, err := intParam(r, "depth", 2, 1, 12)
	if err != nil {
		return err
	}
	minimum, err := intParam(r, "minimum", 10, 1, 1000000)
	if err != nil {
		return err
	}
	offset, err := intParam(r, "offset", 0, 0, 1000000)
	if err != nil {
		return err
	}
	limit, err := intParam(r, "limit", 25, 1, 100)
	if err != nil {
		return err
	}
	pairs, dirs, err := l.cachedDirectoryOverlaps(r.Context(), scope, depth, r.URL.Query().Get("refresh") == "1")
	if err != nil {
		return err
	}
	type overlapPair struct {
		First  directoryOverlap `json:"first"`
		Second directoryOverlap `json:"second"`
	}
	grouped := map[[2]string]*overlapPair{}
	ordered := [][2]string{}
	for _, pair := range pairs {
		a, b := pair.Source, pair.Keep
		if a > b {
			a, b = b, a
		}
		key := [2]string{a, b}
		if grouped[key] == nil {
			grouped[key] = &overlapPair{}
			ordered = append(ordered, key)
		}
		if pair.Source == a {
			grouped[key].First = pair
		} else {
			grouped[key].Second = pair
		}
	}
	filtered := []overlapPair{}
	query := strings.ToLower(strings.TrimSpace(r.URL.Query().Get("q")))
	for _, key := range ordered {
		group := grouped[key]
		pair := group.First
		if query != "" && !strings.Contains(strings.ToLower(pair.Source+" "+pair.Keep), query) {
			continue
		}
		if r.URL.Query().Get("full") == "true" && !pair.Full && !group.Second.Full {
			continue
		}
		if pair.ContentFiles >= minimum || group.Second.ContentFiles >= minimum {
			filtered = append(filtered, *group)
		}
	}
	total := len(filtered)
	start := min(offset, total)
	return writeJSON(w, 200, map[string]any{"items": filtered[start:min(start+limit, total)], "total": total, "directories": dirs, "scope": scope, "depth": depth})
}

type duplicatePlanItem struct {
	RemoveID   string `json:"remove_id"`
	KeepID     string `json:"keep_id"`
	Hash       string `json:"hash"`
	RemovePath string `json:"remove_path"`
	KeepPath   string `json:"keep_path"`
	Size       int64  `json:"size"`
}
type duplicateDirectoryPlan struct {
	Source       string              `json:"source"`
	Keep         string              `json:"keep"`
	SourceFiles  int                 `json:"source_files"`
	KeepFiles    int                 `json:"keep_files"`
	PendingFiles int                 `json:"pending_files"`
	Variants     int                 `json:"variants"`
	Unique       int                 `json:"unique"`
	Bytes        int64               `json:"bytes"`
	Items        []duplicatePlanItem `json:"items"`
}

func (l *Library) buildDirectoryPlan(ctx context.Context, source, keep string) (duplicateDirectoryPlan, error) {
	plan := duplicateDirectoryPlan{Source: source, Keep: keep, Items: []duplicatePlanItem{}}
	if !disjointDirectories(source, keep) {
		return plan, bad(400, "Choose separate directory trees; a parent and its child cannot be compared for cleanup")
	}
	files, err := l.overlapInventory(ctx, source, keep)
	if err != nil {
		return plan, err
	}
	retained := map[string]overlapFile{}
	assets := map[string]bool{}
	for _, f := range files {
		if directoryContains(keep, f.Path) {
			plan.KeepFiles++
			if f.Hash != "" {
				previous, exists := retained[f.Hash]
				if !exists || f.Path < previous.Path {
					retained[f.Hash] = f
				}
			}
			if f.Asset != "" {
				assets[f.Asset] = true
			}
		}
	}
	for _, f := range files {
		if !directoryContains(source, f.Path) {
			continue
		}
		plan.SourceFiles++
		if f.Hash == "" {
			plan.PendingFiles++
			continue
		}
		if match, ok := retained[f.Hash]; ok {
			plan.Items = append(plan.Items, duplicatePlanItem{f.ID, match.ID, f.Hash, f.Path, match.Path, f.Size})
			plan.Bytes += f.Size
		} else if f.Asset != "" && assets[f.Asset] {
			plan.Variants++
		} else {
			plan.Unique++
		}
	}
	sort.Slice(plan.Items, func(i, j int) bool { return plan.Items[i].RemovePath < plan.Items[j].RemovePath })
	if plan.SourceFiles == 0 || plan.KeepFiles == 0 {
		return plan, bad(400, fmt.Sprintf("No indexed photos found in %s or %s", path.Clean(source), path.Clean(keep)))
	}
	return plan, nil
}

type cachedDirectoryOverlaps struct {
	scope    string
	depth    int
	revision uint64
	expires  time.Time
	pairs    []directoryOverlap
	dirs     int
}

func (l *Library) cachedDirectoryOverlaps(ctx context.Context, scope string, depth int, force bool) ([]directoryOverlap, int, error) {
	l.overlapMu.Lock()
	defer l.overlapMu.Unlock()
	var revision uint64
	if l.ai != nil {
		revision = l.ai.selectionRevision.Load()
	}
	if c := l.overlapCache; c != nil && !force && c.scope == scope && c.depth == depth && c.revision == revision && time.Now().Before(c.expires) {
		return c.pairs, c.dirs, nil
	}
	files, err := l.overlapInventory(ctx, scope)
	if err != nil {
		return nil, 0, err
	}
	pairs, dirs, err := analyzeDirectoryOverlaps(ctx, files, scope, depth)
	if err != nil {
		return nil, 0, err
	}
	l.overlapCache = &cachedDirectoryOverlaps{scope, depth, revision, time.Now().Add(45 * time.Second), pairs, dirs}
	return pairs, dirs, nil
}

// directoryVariants returns file pairs with the same image-data identity but
// different full-file hashes. Each pair is inspectable; none is deleted.
func (l *Library) directoryVariants(w http.ResponseWriter, r *http.Request) error {
	a, err := cleanOverlapDir(r.URL.Query().Get("a"))
	if err != nil {
		return err
	}
	b, err := cleanOverlapDir(r.URL.Query().Get("b"))
	if err != nil {
		return err
	}
	if !disjointDirectories(a, b) {
		return bad(400, "Choose separate directory trees")
	}
	offset, err := intParam(r, "offset", 0, 0, 1000000)
	if err != nil {
		return err
	}
	const limit = 20
	rows, err := l.read.QueryContext(r.Context(), `SELECT a.id,a.path,a.size_bytes,metadata_json(a.metadata),b.id,b.path,b.size_bytes,metadata_json(b.metadata)
 FROM images a JOIN images b ON b.content_id=a.content_id AND b.hash_signature=b.signature
 WHERE a.path>=? COLLATE BINARY AND a.path<? COLLATE BINARY AND b.path>=? COLLATE BINARY AND b.path<? COLLATE BINARY
 AND a.hash_signature=a.signature AND a.content_id IS NOT NULL AND a.file_hash IS NOT NULL AND b.file_hash IS NOT NULL AND a.file_hash!=b.file_hash
 ORDER BY a.path,b.path LIMIT ? OFFSET ?`, a+"/", a+"0", b+"/", b+"0", limit+1, offset)
	if err != nil {
		return err
	}
	defer rows.Close()
	type pair struct {
		First  Photo `json:"first"`
		Second Photo `json:"second"`
	}
	items := []pair{}
	for rows.Next() {
		var aid, ap, bid, bp, am, bm string
		var as, bs int64
		if err = rows.Scan(&aid, &ap, &as, &am, &bid, &bp, &bs, &bm); err != nil {
			return err
		}
		var p pair
		if err = json.Unmarshal([]byte(am), &p.First); err != nil {
			return err
		}
		if err = json.Unmarshal([]byte(bm), &p.Second); err != nil {
			return err
		}
		p.First.ID, p.First.Path, p.First.Size = aid, ap, as
		p.Second.ID, p.Second.Path, p.Second.Size = bid, bp, bs
		items = append(items, p)
	}
	if err = rows.Err(); err != nil {
		return err
	}
	more := len(items) > limit
	if more {
		items = items[:limit]
	}
	return writeJSON(w, 200, map[string]any{"items": items, "more": more})
}
