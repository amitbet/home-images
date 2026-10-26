package main

import (
	"crypto/sha256"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net/http"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"
)

type endpoint func(http.ResponseWriter, *http.Request) error

func writeJSON(w http.ResponseWriter, status int, value any) error {
	b, err := json.Marshal(value)
	if err != nil {
		return err
	}
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_, err = w.Write(append(b, '\n'))
	return err
}
func (l *Library) Handler() http.Handler {
	mux := http.NewServeMux()
	wrap := func(fn endpoint) http.HandlerFunc {
		return func(w http.ResponseWriter, r *http.Request) {
			if r.Method != http.MethodGet && r.Method != http.MethodHead {
				w.Header().Set("Allow", "GET, HEAD, OPTIONS")
				_ = writeJSON(w, 405, map[string]string{"detail": "Method not allowed"})
				return
			}
			if err := fn(w, r); err != nil {
				status, text := 500, "Internal server error"
				var api *apiError
				if errors.As(err, &api) {
					status, text = api.code, api.text
				} else {
					log.Print(err)
				}
				_ = writeJSON(w, status, map[string]string{"detail": text})
			}
		}
	}
	mux.HandleFunc("/health", wrap(l.health))
	mux.Handle("/app/", http.StripPrefix("/app/", webHandler))
	l.ai.routes(mux, wrap)
	mux.HandleFunc("/api/directories", wrap(l.directories))
	mux.HandleFunc("/api/images", wrap(l.images))
	mux.HandleFunc("/api/next", wrap(l.next))
	mux.HandleFunc("/api/images/{id}", wrap(l.imageMetadata))
	mux.HandleFunc("/api/images/{id}/image", wrap(l.render))
	mux.HandleFunc("/api/images/{id}/thumbnail", wrap(l.thumbnail))
	mux.HandleFunc("/api/images/{id}/original", wrap(l.original))
	mux.HandleFunc("/docs", wrap(func(w http.ResponseWriter, r *http.Request) error {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		_, err := fmt.Fprint(w, apiDocs)
		return err
	}))
	mux.HandleFunc("/", wrap(func(w http.ResponseWriter, r *http.Request) error {
		if r.URL.Path != "/" {
			return bad(404, "Not found")
		}
		return serveUI(w, r)
	}))
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Content-Type-Options", "nosniff")
		origin := r.Header.Get("Origin")
		if origin != "" {
			w.Header().Add("Vary", "Origin")
			for _, allowed := range l.config.CORSOrigins {
				if allowed == "*" || allowed == origin {
					w.Header().Set("Access-Control-Allow-Origin", allowed)
					w.Header().Set("Access-Control-Allow-Methods", "GET, HEAD, OPTIONS")
					w.Header().Set("Access-Control-Allow-Headers", "If-None-Match, Content-Type")
					w.Header().Set("Access-Control-Expose-Headers", "ETag")
					break
				}
			}
		}
		if r.Method == http.MethodOptions {
			w.WriteHeader(204)
			return
		}
		mux.ServeHTTP(w, r)
	})
}
func intParam(r *http.Request, key string, def, min, max int) (int, error) {
	value := r.URL.Query().Get(key)
	if value == "" {
		return def, nil
	}
	n, e := strconv.Atoi(value)
	if e != nil || n < min || n > max {
		return 0, bad(422, fmt.Sprintf("Invalid %s (range %d–%d)", key, min, max))
	}
	return n, nil
}
func (l *Library) health(w http.ResponseWriter, r *http.Request) error {
	var count int
	if err := l.db.QueryRowContext(r.Context(), "SELECT count(*) FROM images").Scan(&count); err != nil {
		return err
	}
	status := l.Status()
	return writeJSON(w, 200, map[string]any{"status": "ok", "implementation": "go", "catalog_version": catalogVersion, "version": version, "images": count, "root_available": fileAvailable(l.rootPath), "scanning": status.Scanning, "last_scan": status.LastScan, "errors": status.Errors})
}
func (l *Library) directories(w http.ResponseWriter, r *http.Request) error {
	directory, err := l.directory(r.URL.Query().Get("directory"))
	if err != nil {
		return err
	}
	offset, err := intParam(r, "offset", 0, 0, 1_000_000_000)
	if err != nil {
		return err
	}
	limit, err := intParam(r, "limit", 100, 1, 500)
	if err != nil {
		return err
	}
	f, err := l.open(directory)
	if err != nil {
		return err
	}
	defer f.Close()
	entries, err := f.ReadDir(-1)
	if err != nil {
		return err
	}
	children := []string{}
	for _, d := range entries {
		if d.IsDir() {
			relative := filepath.ToSlash(filepath.Join(directory, d.Name()))
			if _, e := l.directory(relative); e == nil {
				children = append(children, relative)
			}
		}
	}
	sort.Slice(children, func(i, j int) bool { return strings.ToLower(children[i]) < strings.ToLower(children[j]) })
	total := len(children)
	if offset > total {
		offset = total
	}
	end := min(offset+limit, total)
	return writeJSON(w, 200, map[string]any{"directory": directory, "total": total, "directories": children[offset:end]})
}
func escapeLike(s string) string {
	return strings.NewReplacer("!", "!!", "%", "!%", "_", "!_").Replace(s)
}
func (l *Library) selection(r *http.Request) (string, []any, error) {
	directory, err := l.directory(r.URL.Query().Get("directory"))
	if err != nil {
		return "", nil, err
	}
	recursive := true
	if value := r.URL.Query().Get("recursive"); value != "" {
		recursive, err = strconv.ParseBool(value)
		if err != nil {
			return "", nil, bad(422, "Invalid recursive boolean")
		}
	}
	clause := "directory = ?"
	args := []any{directory}
	if recursive {
		prefix := "%"
		if directory != "" {
			prefix = escapeLike(directory+"/") + "%"
		}
		clause = "path LIKE ? ESCAPE '!'"
		args = []any{prefix}
	}
	q := r.URL.Query().Get("q")
	if utf8.RuneCountInString(q) > 500 {
		return "", nil, bad(422, "q is too long")
	}
	if q != "" {
		pattern := "%" + escapeLike(strings.ToLower(q)) + "%"
		clause += ` AND (search LIKE ? ESCAPE '!' OR id IN(SELECT image_id FROM curation.annotations WHERE root=? AND lower(title||' '||description||' '||location||' '||tags) LIKE ? ESCAPE '!'))`
		args = append(args, pattern, l.rootPath, pattern)
	}
	return l.curationSelection(r, clause, args)
}
func (l *Library) images(w http.ResponseWriter, r *http.Request) error {
	clause, args, err := l.selection(r)
	if err != nil {
		return err
	}
	offset, err := intParam(r, "offset", 0, 0, 1_000_000_000)
	if err != nil {
		return err
	}
	limit, err := intParam(r, "limit", 50, 1, 200)
	if err != nil {
		return err
	}
	var count int
	if err = l.db.QueryRowContext(r.Context(), "SELECT count(*) FROM images WHERE "+clause, args...).Scan(&count); err != nil {
		return err
	}
	rows, err := l.db.QueryContext(r.Context(), "SELECT metadata_json(metadata) FROM images WHERE "+clause+" ORDER BY path COLLATE NOCASE,path LIMIT ? OFFSET ?", append(args, limit, offset)...)
	if err != nil {
		return err
	}
	defer rows.Close()
	items := []Photo{}
	for rows.Next() {
		var encoded string
		if err = rows.Scan(&encoded); err != nil {
			return err
		}
		var p Photo
		if err = json.Unmarshal([]byte(encoded), &p); err != nil {
			return err
		}
		items = append(items, p)
	}
	if err = rows.Err(); err != nil {
		return err
	}
	return writeJSON(w, 200, map[string]any{"total": count, "offset": offset, "items": items})
}
func (l *Library) next(w http.ResponseWriter, r *http.Request) error {
	clause, args, err := l.selection(r)
	if err != nil {
		return err
	}
	mode := r.URL.Query().Get("mode")
	if mode == "" {
		mode = "random"
	}
	if mode != "random" && mode != "sequential" {
		return bad(422, "mode must be random or sequential")
	}
	after := r.URL.Query().Get("after")
	var id string
	if mode == "random" {
		err = l.db.QueryRowContext(r.Context(), "SELECT id FROM images WHERE "+clause+" AND id != ? ORDER BY random() LIMIT 1", append(args, after)...).Scan(&id)
		if errors.Is(err, sql.ErrNoRows) {
			err = l.db.QueryRowContext(r.Context(), "SELECT id FROM images WHERE "+clause+" LIMIT 1", args...).Scan(&id)
		}
	} else {
		var previous string
		previousErr := l.db.QueryRowContext(r.Context(), "SELECT path FROM images WHERE id=?", after).Scan(&previous)
		if previousErr != nil && !errors.Is(previousErr, sql.ErrNoRows) {
			return previousErr
		}
		err = sql.ErrNoRows
		if previousErr == nil {
			err = l.db.QueryRowContext(r.Context(), "SELECT id FROM images WHERE "+clause+" AND (path COLLATE NOCASE,path) > (?,?) ORDER BY path COLLATE NOCASE,path LIMIT 1", append(args, previous, previous)...).Scan(&id)
		}
		if errors.Is(err, sql.ErrNoRows) {
			err = l.db.QueryRowContext(r.Context(), "SELECT id FROM images WHERE "+clause+" ORDER BY path COLLATE NOCASE,path LIMIT 1", args...).Scan(&id)
		}
	}
	if errors.Is(err, sql.ErrNoRows) {
		return bad(404, "No indexed images match; initial indexing may still be running")
	}
	if err != nil {
		return err
	}
	p, err := l.get(r.Context(), id)
	if err != nil {
		return err
	}
	return writeJSON(w, 200, p)
}
func (l *Library) imageMetadata(w http.ResponseWriter, r *http.Request) error {
	p, e := l.get(r.Context(), r.PathValue("id"))
	if e != nil {
		return e
	}
	return writeJSON(w, 200, p)
}
func (l *Library) original(w http.ResponseWriter, r *http.Request) error {
	p, err := l.get(r.Context(), r.PathValue("id"))
	if err != nil {
		return err
	}
	f, err := l.open(p.Path)
	if err != nil {
		return err
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil {
		return err
	}
	http.ServeContent(w, r, p.Filename, info.ModTime(), f)
	return nil
}
func (l *Library) render(w http.ResponseWriter, r *http.Request) error {
	l.lastRender.Store(time.Now().UnixNano())
	width, err := intParam(r, "width", 1280, 32, 2560)
	if err != nil {
		return err
	}
	height, err := intParam(r, "height", 800, 32, 2560)
	if err != nil {
		return err
	}
	quality, err := intParam(r, "quality", 85, 40, 95)
	if err != nil {
		return err
	}
	p, err := l.get(r.Context(), r.PathValue("id"))
	if err != nil {
		return err
	}
	f, err := l.open(p.Path)
	if err != nil {
		return err
	}
	info, err := f.Stat()
	f.Close()
	if err != nil {
		return err
	}
	sum := sha256.Sum256(fmt.Appendf(nil, "%s:%d:%d:%d:%d:%d", p.ID, info.ModTime().UnixNano(), info.Size(), width, height, quality))
	etag := fmt.Sprintf(`"%x"`, sum)
	if r.Header.Get("If-None-Match") == etag {
		w.Header().Set("ETag", etag)
		w.WriteHeader(304)
		return nil
	}
	data, err := l.jpeg(r.Context(), p, width, height, quality)
	if err != nil {
		return err
	}
	w.Header().Set("ETag", etag)
	w.Header().Set("Cache-Control", "private, max-age=300")
	w.Header().Set("Content-Type", "image/jpeg")
	w.Header().Set("Content-Length", strconv.Itoa(len(data)))
	if r.Method == http.MethodHead {
		return nil
	}
	_, err = w.Write(data)
	return err
}

const apiDocs = `<!doctype html><html lang="en"><meta charset="utf-8"><meta name="viewport" content="width=device-width"><title>Home Images API</title>
<style>body{font:17px system-ui;max-width:850px;margin:40px auto;padding:20px;line-height:1.6}code{background:#eee;padding:3px}li{margin:12px 0}</style>
<h1>Home Images API</h1><p>Read-only photo API. Query paths use forward slashes relative to the configured media root.</p><ul>
<li><a href="/health">GET /health</a> — image count and scan status</li>
<li><a href="/api/directories">GET /api/directories</a>?directory=ALBUM&amp;offset=0&amp;limit=100</li>
<li><a href="/api/images?limit=5">GET /api/images</a>?directory=ALBUM&amp;recursive=true&amp;q=Paris&amp;offset=0&amp;limit=50</li>
<li><a href="/api/next?mode=random">GET /api/next</a>?mode=random|sequential&amp;directory=ALBUM&amp;recursive=true&amp;after=PREVIOUS_ID&amp;q=Paris</li>
<li><a href="/api/playback/next?order=random">GET /api/playback/next</a>?order=random|sequential&amp;mode=semantic|metadata&amp;q=beach&amp;person=12&amp;album=3&amp;collection=7&amp;after=PREVIOUS_ID — album/search playback; semantic pool defaults to top 500 matches, cached for 120 seconds.</li>
<li><a href="/api/people">GET /api/people</a> — named face groups; POST /api/people/{id} with name or merge_into.</li>
<li><a href="/api/indexing">GET /api/indexing</a> — AI progress; POST /api/indexing/control with action pause, resume or retry.</li>
<li>GET/POST /api/curation/{id} — favorites, hidden state, title, description, location and tags. GET/POST /api/albums and /api/collections — virtual albums and saved search rules. Writes require JSON, X-Home-Images: 1 and same-origin browser requests.</li>
<li>GET /api/images/{id} — metadata</li><li>GET /api/images/{id}/image?width=1280&amp;height=800&amp;quality=85 — upright JPEG</li>
<li>GET /api/images/{id}/original — original file, including range requests</li></ul>
<p>Selection returns metadata and relative <code>image_url</code>/<code>original_url</code>. Prefix those URLs with this API's origin.
Sequential order is alphabetical by path and wraps; random selection avoids the previous ID when possible. Search matches existing path and EXIF text, not image contents.</p>
<p>Filesystem creation dates and camera timestamps are separate. Camera timestamps may have no timezone. HEIC display requires optional FFmpeg.</p>
<p>See the <a href="https://github.com/amitbet/home-images">source and deployment instructions</a>.</p></html>`
