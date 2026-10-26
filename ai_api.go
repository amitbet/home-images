package main

import (
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"
)

func (a *AI) routes(mux *http.ServeMux, wrap func(endpoint) http.HandlerFunc) {
	a.curationRoutes(mux, wrap)
	mux.HandleFunc("/api/indexing", wrap(a.indexStatus))
	mux.HandleFunc("/api/search", wrap(a.search))
	mux.HandleFunc("/api/playback/next", wrap(a.playbackNext))
	mux.HandleFunc("/api/people", wrap(a.people))
	mux.HandleFunc("/api/people/{person}/image", wrap(a.faceImage))
	mux.HandleFunc("/api/images/{id}/faces", wrap(a.imageFaces))
	mux.HandleFunc("/api/indexing/control", a.mutation(a.control))
	mux.HandleFunc("/api/people/{person}", a.mutation(a.editPerson))
	mux.HandleFunc("/api/faces", a.mutation(a.batchFaces))
	mux.HandleFunc("/api/faces/{face}", a.mutation(a.editFace))
}

// Require a custom header and same-origin writes. The existing read-only LAN
// API's wildcard CORS policy must not grant arbitrary websites write access.
func (a *AI) mutation(fn endpoint) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != "POST" {
			_ = writeJSON(w, 405, map[string]string{"detail": "Use POST"})
			return
		}
		origin := r.Header.Get("Origin")
		if r.Header.Get("X-Home-Images") != "1" {
			_ = writeJSON(w, 403, map[string]string{"detail": "Missing request header"})
			return
		}
		if origin != "" {
			u, err := url.Parse(origin)
			scheme := "http"
			if r.TLS != nil {
				scheme = "https"
			}
			if err != nil || u.Host != r.Host || u.Scheme != scheme {
				_ = writeJSON(w, 403, map[string]string{"detail": "Same-origin request required"})
				return
			}
		}
		r.Body = http.MaxBytesReader(w, r.Body, 16<<10)
		// Invalidate snapshots on API edits even if the client disconnects after
		// a committed write. This revision is independent of pooled connections.
		if r.URL.Path != "/api/curation" {
			defer a.selectionRevision.Add(1)
		} // This batch endpoint only reads annotations.
		if err := fn(w, r); err != nil {
			status, msg := 500, "Request failed"
			var e *apiError
			if errors.As(err, &e) {
				status, msg = e.code, e.text
			}
			_ = writeJSON(w, status, map[string]string{"detail": msg})
		}
	}
}
func (a *AI) indexStatus(w http.ResponseWriter, r *http.Request) error {
	var total, indexed, failed, faces, people int
	if err := a.db.QueryRowContext(r.Context(), "SELECT count(*) FROM catalog.images").Scan(&total); err != nil {
		return err
	}
	if err := a.db.QueryRowContext(r.Context(), `SELECT count(*),coalesce(sum(CASE WHEN a.error!='' THEN 1 ELSE 0 END),0) FROM ai_images a JOIN catalog.images i ON i.id=a.image_id AND i.signature=a.signature WHERE a.models=?`, a.models).Scan(&indexed, &failed); err != nil {
		return err
	}
	if err := a.db.QueryRowContext(r.Context(), `SELECT count(*),count(DISTINCT f.person_id) FROM faces f JOIN ai_images a ON a.image_id=f.image_id JOIN catalog.images i ON i.id=a.image_id AND i.signature=a.signature WHERE a.models=? AND a.error=''`, a.models).Scan(&faces, &people); err != nil {
		return err
	}
	a.mu.Lock()
	active, lastErr, lastSuccess, online := a.active, a.lastError, a.lastSuccess, a.online
	a.mu.Unlock()
	return writeJSON(w, 200, map[string]any{"enabled": a.lib.config.ML.Enabled, "paused": a.paused.Load(), "worker_url": a.lib.config.ML.URL, "worker_online": online, "active": active, "total": total, "indexed": indexed - failed, "failed": failed, "pending": total - indexed, "faces": faces, "people": people, "last_error": lastErr, "last_success": lastSuccess, "clip_model": a.lib.config.ML.ClipModel, "face_model": a.lib.config.ML.FaceModel, "interval_ms": a.lib.config.ML.IntervalMS})
}
func (a *AI) control(w http.ResponseWriter, r *http.Request) error {
	var body struct {
		Action string `json:"action"`
	}
	if json.NewDecoder(r.Body).Decode(&body) != nil {
		return bad(400, "Invalid JSON")
	}
	switch body.Action {
	case "pause", "resume":
		paused := body.Action == "pause"
		if _, err := a.db.ExecContext(r.Context(), "INSERT OR REPLACE INTO ai_settings VALUES('paused',?)", strconv.FormatBool(paused)); err != nil {
			return err
		}
		a.paused.Store(paused)
	case "retry":
		if _, err := a.db.ExecContext(r.Context(), "UPDATE ai_images SET retry_at=0 WHERE error!=''"); err != nil {
			return err
		}
	default:
		return bad(400, "action must be pause, resume, or retry")
	}
	return writeJSON(w, 200, map[string]any{"paused": a.paused.Load()})
}
func personID(r *http.Request) (int64, error) {
	id, err := strconv.ParseInt(r.PathValue("person"), 10, 64)
	if err != nil || id < 1 {
		return 0, bad(400, "Invalid person")
	}
	return id, nil
}
func (a *AI) editPerson(w http.ResponseWriter, r *http.Request) error {
	id, err := personID(r)
	if err != nil {
		return err
	}
	var body struct {
		Name      *string `json:"name"`
		MergeInto int64   `json:"merge_into"`
	}
	if json.NewDecoder(r.Body).Decode(&body) != nil {
		return bad(400, "Invalid JSON")
	}
	if body.Name != nil && body.MergeInto != 0 {
		return bad(400, "Rename or merge in separate requests")
	}
	tx, err := a.db.BeginTx(r.Context(), nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var model string
	if err = tx.QueryRow("SELECT model FROM people WHERE id=?", id).Scan(&model); errors.Is(err, sql.ErrNoRows) {
		return bad(404, "Person not found")
	} else if err != nil {
		return err
	}
	if body.MergeInto != 0 {
		if body.MergeInto == id {
			return bad(400, "Choose a different group")
		}
		var targetModel string
		if err = tx.QueryRow("SELECT model FROM people WHERE id=?", body.MergeInto).Scan(&targetModel); errors.Is(err, sql.ErrNoRows) {
			return bad(404, "Target person not found")
		} else if err != nil {
			return err
		}
		if model != targetModel {
			return bad(400, "Groups from different face models cannot be merged")
		}
		// Keep source representatives as aliases so future photos match either group.
		if _, err = tx.Exec("UPDATE faces SET person_id=? WHERE person_id=?", body.MergeInto, id); err != nil {
			return err
		}
		if _, err = tx.Exec("UPDATE people SET name='', merged_into=? WHERE id=?", body.MergeInto, id); err != nil {
			return err
		}
		if _, err = tx.Exec("UPDATE people SET merged_into=? WHERE merged_into=?", body.MergeInto, id); err != nil {
			return err
		}
	} else if body.Name != nil {
		name := strings.TrimSpace(*body.Name)
		if utf8.RuneCountInString(name) > 100 {
			return bad(400, "Name must be at most 100 characters")
		}
		if _, err = tx.Exec("UPDATE people SET name=? WHERE id=? AND merged_into IS NULL", name, id); err != nil {
			return err
		}
	} else {
		return bad(400, "Provide name or merge_into")
	}
	if err = tx.Commit(); err != nil {
		return err
	}
	return writeJSON(w, 200, map[string]any{"ok": true})
}

// Retag one face without changing the rest of its group. A name selects the
// existing unmerged person with that name (case-insensitive), or creates one.
func (a *AI) editFace(w http.ResponseWriter, r *http.Request) error {
	faceID, err := strconv.ParseInt(r.PathValue("face"), 10, 64)
	if err != nil || faceID < 1 {
		return bad(400, "Invalid face")
	}
	var body struct {
		Name     *string `json:"name"`
		PersonID int64   `json:"person_id"`
	}
	if json.NewDecoder(r.Body).Decode(&body) != nil {
		return bad(400, "Invalid JSON")
	}
	if (body.Name != nil) == (body.PersonID != 0) {
		return bad(400, "Provide name or person_id")
	}
	tx, err := a.db.BeginTx(r.Context(), nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var imageID, model string
	var current int64
	var embedding []byte
	if err = tx.QueryRow("SELECT f.image_id,f.person_id,f.embedding,p.model FROM faces f JOIN people p ON p.id=f.person_id WHERE f.id=?", faceID).Scan(&imageID, &current, &embedding, &model); errors.Is(err, sql.ErrNoRows) {
		return bad(404, "Face not found")
	} else if err != nil {
		return err
	}
	var target int64
	var name string
	created := false
	if body.PersonID != 0 {
		var targetModel string
		if err = tx.QueryRow("SELECT coalesce(merged_into,id) FROM people WHERE id=?", body.PersonID).Scan(&target); errors.Is(err, sql.ErrNoRows) {
			return bad(404, "Person not found")
		} else if err != nil {
			return err
		}
		if err = tx.QueryRow("SELECT name,model FROM people WHERE id=?", target).Scan(&name, &targetModel); err != nil {
			return err
		}
		if targetModel != model {
			return bad(400, "Groups from different face models cannot be combined")
		}
	} else {
		name = strings.TrimSpace(*body.Name)
		if name == "" || utf8.RuneCountInString(name) > 100 {
			return bad(400, "Name must be 1 to 100 characters")
		}
		// Prefer the largest group when several share a name.
		err = tx.QueryRow("SELECT p.id,p.name FROM people p LEFT JOIN faces f ON f.person_id=p.id WHERE p.model=? AND p.merged_into IS NULL AND p.name=? COLLATE NOCASE GROUP BY p.id ORDER BY count(f.id) DESC,p.id LIMIT 1", model, name).Scan(&target, &name)
		if errors.Is(err, sql.ErrNoRows) {
			// Faces indexed before per-face embeddings get an empty
			// representative, which never matches new photos automatically.
			result, e := tx.Exec("INSERT INTO people(name,model,embedding) VALUES(?,?,?)", name, model, nonNil(embedding))
			if e != nil {
				return e
			}
			if target, err = result.LastInsertId(); err != nil {
				return err
			}
			created = true
		} else if err != nil {
			return err
		}
	}
	if target != current {
		var taken int
		if err = tx.QueryRow("SELECT count(*) FROM faces WHERE image_id=? AND person_id=? AND id!=?", imageID, target, faceID).Scan(&taken); err != nil {
			return err
		}
		if taken > 0 {
			return bad(409, "That person is already tagged in this photo")
		}
		if _, err = tx.Exec("UPDATE faces SET person_id=? WHERE id=?", target, faceID); err != nil {
			return err
		}
		// An alias representative, as merges keep, so similar faces in new
		// photos join the corrected person.
		if !created && len(embedding) > 0 {
			if _, err = tx.Exec("INSERT INTO people(name,model,embedding,merged_into) VALUES('',?,?,?)", model, embedding, target); err != nil {
				return err
			}
		}
	}
	if err = tx.Commit(); err != nil {
		return err
	}
	return writeJSON(w, 200, map[string]any{"ok": true, "person_id": target, "name": name})
}
func nonNil(b []byte) []byte {
	if b == nil {
		return []byte{}
	}
	return b
}

type personSummary struct {
	ID       int64  `json:"id"`
	Name     string `json:"name"`
	Count    int    `json:"photo_count"`
	ImageURL string `json:"image_url"`
}

func (a *AI) people(w http.ResponseWriter, r *http.Request) error {
	limit, err := intParam(r, "limit", 48, 1, 200)
	if err != nil {
		return err
	}
	offset, err := intParam(r, "offset", 0, 0, 1000000)
	if err != nil {
		return err
	}
	q := strings.TrimSpace(r.URL.Query().Get("q"))
	if utf8.RuneCountInString(q) > 100 {
		return bad(400, "Name query too long")
	}
	base := ` FROM people p JOIN faces f ON f.person_id=p.id JOIN ai_images a ON a.image_id=f.image_id JOIN catalog.images i ON i.id=a.image_id AND i.signature=a.signature WHERE a.models=? AND a.error='' AND p.merged_into IS NULL AND p.name LIKE ? ESCAPE '!'`
	args := []any{a.models, "%" + escapeLike(q) + "%"}
	visible, visibilityArgs, err := a.lib.curationSelection(r, "1=1", nil)
	if err != nil {
		return err
	}
	base += " AND i.id IN(SELECT id FROM catalog.images WHERE " + visible + ")"
	args = append(args, visibilityArgs...)
	var total int
	if err = a.read.QueryRowContext(r.Context(), "SELECT count(DISTINCT p.id)"+base, args...).Scan(&total); err != nil {
		return err
	}
	rows, err := a.read.QueryContext(r.Context(), "SELECT p.id,p.name,count(DISTINCT f.image_id)"+base+" GROUP BY p.id ORDER BY (p.name!='') DESC,count(DISTINCT f.image_id) DESC,p.id LIMIT ? OFFSET ?", append(args, limit, offset)...)
	if err != nil {
		return err
	}
	defer rows.Close()
	items := []personSummary{}
	for rows.Next() {
		var p personSummary
		if err = rows.Scan(&p.ID, &p.Name, &p.Count); err != nil {
			return err
		}
		p.ImageURL = fmt.Sprintf("/api/people/%d/image", p.ID)
		if r.URL.Query().Get("hidden") == "all" || r.URL.Query().Get("hidden") == "only" {
			p.ImageURL += "?hidden=" + r.URL.Query().Get("hidden")
		}
		items = append(items, p)
	}
	if err = rows.Err(); err != nil {
		return err
	}
	return writeJSON(w, 200, map[string]any{"items": items, "total": total, "offset": offset})
}

type faceTag struct {
	ID       int64           `json:"id"`
	PersonID int64           `json:"person_id"`
	Name     string          `json:"name"`
	Box      json.RawMessage `json:"box"`
	Width    int             `json:"width"`
	Height   int             `json:"height"`
}

// Indexed faces for the given images, keyed by image ID, honoring the hidden filter.
func (a *AI) faceTags(r *http.Request, ids []string) (map[string][]faceTag, error) {
	result := map[string][]faceTag{}
	if len(ids) == 0 {
		return result, nil
	}
	visible, args, err := a.lib.curationSelection(r, "1=1", nil)
	if err != nil {
		return nil, err
	}
	query := []any{a.models}
	for _, id := range ids {
		query = append(query, id)
	}
	rows, err := a.read.QueryContext(r.Context(), `SELECT f.image_id,f.id,f.person_id,p.name,f.box,f.width,f.height FROM faces f JOIN people p ON p.id=f.person_id JOIN ai_images a ON a.image_id=f.image_id JOIN catalog.images i ON i.id=a.image_id AND i.signature=a.signature WHERE a.models=? AND f.image_id IN(?`+strings.Repeat(",?", len(ids)-1)+`) AND a.error='' AND i.id IN(SELECT id FROM catalog.images WHERE `+visible+`) ORDER BY f.id`, append(query, args...)...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var image, box string
		var f faceTag
		if err = rows.Scan(&image, &f.ID, &f.PersonID, &f.Name, &box, &f.Width, &f.Height); err != nil {
			return nil, err
		}
		f.Box = json.RawMessage(box)
		result[image] = append(result[image], f)
	}
	return result, rows.Err()
}
func (a *AI) imageFaces(w http.ResponseWriter, r *http.Request) error {
	tags, err := a.faceTags(r, []string{r.PathValue("id")})
	if err != nil {
		return err
	}
	return writeJSON(w, 200, append([]faceTag{}, tags[r.PathValue("id")]...))
}
func (a *AI) batchFaces(w http.ResponseWriter, r *http.Request) error {
	var body struct {
		IDs []string `json:"ids"`
	}
	if json.NewDecoder(r.Body).Decode(&body) != nil || len(body.IDs) > 60 {
		return bad(400, "Provide up to 60 image IDs")
	}
	tags, err := a.faceTags(r, body.IDs)
	if err != nil {
		return err
	}
	return writeJSON(w, 200, tags)
}
func (a *AI) faceImage(w http.ResponseWriter, r *http.Request) error {
	id, err := personID(r)
	if err != nil {
		return err
	}
	var imageID, box string
	var width, height int
	visible, args, err := a.lib.curationSelection(r, "1=1", nil)
	if err != nil {
		return err
	}
	err = a.read.QueryRowContext(r.Context(), `SELECT f.image_id,f.box,f.width,f.height FROM faces f JOIN ai_images a ON a.image_id=f.image_id JOIN catalog.images i ON i.id=a.image_id AND i.signature=a.signature WHERE f.person_id=? AND a.models=? AND a.error='' AND i.id IN(SELECT id FROM catalog.images WHERE `+visible+`) ORDER BY f.score DESC,f.id LIMIT 1`, append([]any{id, a.models}, args...)...).Scan(&imageID, &box, &width, &height)
	if errors.Is(err, sql.ErrNoRows) {
		return bad(404, "No indexed face")
	}
	if err != nil {
		return err
	}
	a.lib.lastRender.Store(time.Now().UnixNano())
	p, err := a.lib.get(r.Context(), imageID)
	if err != nil {
		return err
	}
	data, err := a.lib.faceThumbnail(r.Context(), p, box, width, height)
	if err != nil {
		return err
	}
	w.Header().Set("Content-Type", "image/jpeg")
	w.Header().Set("Cache-Control", "private,max-age=300")
	_, err = w.Write(data)
	return err
}

type ranked struct {
	ID    string
	Score float32
}
type ranking []ranked

func (h ranking) Len() int { return len(h) }
func (h ranking) Less(i, j int) bool {
	if h[i].Score == h[j].Score {
		return h[i].ID > h[j].ID
	}
	return h[i].Score < h[j].Score
}
func (h ranking) Swap(i, j int) { h[i], h[j] = h[j], h[i] }
func (h *ranking) Push(x any)   { *h = append(*h, x.(ranked)) }
func (h *ranking) Pop() any     { old := *h; x := old[len(old)-1]; *h = old[:len(old)-1]; return x }
func (a *AI) search(w http.ResponseWriter, r *http.Request) error {
	limit, err := intParam(r, "limit", 60, 1, 200)
	if err != nil {
		return err
	}
	offset, err := intParam(r, "offset", 0, 0, 1000)
	if err != nil {
		return err
	}
	mode := r.URL.Query().Get("mode")
	if mode == "" {
		mode = "semantic"
	}
	if mode != "semantic" && mode != "metadata" {
		return bad(400, "mode must be semantic or metadata")
	}
	q := strings.TrimSpace(r.URL.Query().Get("q"))
	if utf8.RuneCountInString(q) > 500 {
		return bad(400, "Query too long")
	}
	order, sortArgs, err := a.photoOrder(r)
	if err != nil {
		return err
	}
	clause, args, err := a.playbackFilter(r, mode)
	if err != nil {
		return err
	}
	if mode == "metadata" || q == "" {
		var total int
		if err = a.read.QueryRowContext(r.Context(), "SELECT count(*) FROM catalog.images i WHERE "+clause, args...).Scan(&total); err != nil {
			return err
		}
		rows, e := a.read.QueryContext(r.Context(), "SELECT metadata_json(metadata) FROM catalog.images i WHERE "+clause+" ORDER BY "+order+" LIMIT ? OFFSET ?", append(append(args, sortArgs...), limit, offset)...)
		if e != nil {
			return e
		}
		defer rows.Close()
		items := []Photo{}
		for rows.Next() {
			var raw string
			var p Photo
			if err = rows.Scan(&raw); err != nil {
				return err
			}
			if err = json.Unmarshal([]byte(raw), &p); err != nil {
				return err
			}
			items = append(items, p)
		}
		if err = rows.Err(); err != nil {
			return err
		}
		return writeJSON(w, 200, map[string]any{"items": items, "total": total, "offset": offset, "semantic": false})
	}
	selection, err := a.rankSelection(r.Context(), q, clause, args, offset+limit)
	if err != nil {
		return err
	}
	ordered, total := selection.Hits, selection.Total
	items := []map[string]any{}
	for _, hit := range ordered[min(offset, len(ordered)):] {
		p, e := a.lib.get(r.Context(), hit.ID)
		if e != nil {
			continue
		}
		raw, _ := json.Marshal(p)
		var item map[string]any
		_ = json.Unmarshal(raw, &item)
		item["similarity"] = hit.Score
		items = append(items, item)
	}
	return writeJSON(w, 200, map[string]any{"items": items, "total": min(total, 1200), "indexed_candidates": total, "offset": offset, "semantic": true})
}

// Only allow known expressions and directions; unknown values never reach SQL.
func (a *AI) photoOrder(r *http.Request) (string, []any, error) {
	direction := r.URL.Query().Get("direction")
	if direction == "" {
		direction = "asc"
	}
	if direction != "asc" && direction != "desc" {
		return "", nil, bad(400, "direction must be asc or desc")
	}
	var expr string
	var args []any
	switch r.URL.Query().Get("sort") {
	case "", "filename":
		expr = "i.path"
	case "taken_at":
		expr = "nullif(i.taken_at,'')"
	case "created_at":
		expr = "i.created_at"
	case "size":
		expr = "i.size_bytes"
	case "location":
		expr = "coalesce((SELECT nullif(location,'') FROM curation.annotations WHERE root=? AND image_id=i.id),(SELECT nullif(label,'') FROM catalog.locations g WHERE g.image_id=i.id AND g.signature=i.signature))"
		args = []any{a.lib.rootPath}
	case "person", "people_count":
		value := "min(nullif(p.name,''))"
		if r.URL.Query().Get("sort") == "people_count" {
			value = "count(DISTINCT p.id)"
		}
		expr = "(SELECT " + value + " FROM faces f JOIN people p ON p.id=f.person_id JOIN ai_images ai ON ai.image_id=f.image_id WHERE f.image_id=i.id AND ai.signature=i.signature AND ai.models=? AND ai.error='')"
		args = []any{a.models}
	default:
		return "", nil, bad(400, "Unknown photo sort")
	}
	return expr + " COLLATE NOCASE " + strings.ToUpper(direction) + " NULLS LAST, i.path COLLATE NOCASE, i.path", args, nil
}
