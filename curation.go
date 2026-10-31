package main

// Curation is intentionally separate from the disposable image/ML indexes.
// Every record is scoped to the photo root; scans never delete user annotations.
import (
	"database/sql"
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"
)

func (a *AI) initCuration() error {
	_, err := a.db.Exec(`PRAGMA curation.journal_mode=WAL;
 CREATE TABLE IF NOT EXISTS curation.annotations(root TEXT NOT NULL,image_id TEXT NOT NULL,favorite INTEGER NOT NULL DEFAULT 0,hidden INTEGER NOT NULL DEFAULT 0,title TEXT NOT NULL DEFAULT '',description TEXT NOT NULL DEFAULT '',location TEXT NOT NULL DEFAULT '',tags TEXT NOT NULL DEFAULT '[]',updated_at TEXT NOT NULL,PRIMARY KEY(root,image_id));
 CREATE TABLE IF NOT EXISTS curation.albums(id INTEGER PRIMARY KEY AUTOINCREMENT,root TEXT NOT NULL,name TEXT NOT NULL,created_at TEXT NOT NULL);
 CREATE TABLE IF NOT EXISTS curation.album_images(album_id INTEGER NOT NULL,image_id TEXT NOT NULL,PRIMARY KEY(album_id,image_id));
 CREATE TABLE IF NOT EXISTS curation.collections(id INTEGER PRIMARY KEY AUTOINCREMENT,root TEXT NOT NULL,name TEXT NOT NULL,query TEXT NOT NULL,created_at TEXT NOT NULL);`)
	return err
}

func (a *AI) curationRoutes(mux *http.ServeMux, wrap func(endpoint) http.HandlerFunc) {
	mux.HandleFunc("GET /api/curation/export", wrap(a.exportCuration))
	mux.HandleFunc("GET /api/curation/{id}", wrap(a.getAnnotation))
	mux.HandleFunc("POST /api/curation/{id}", a.mutation(a.editAnnotation))
	mux.HandleFunc("POST /api/curation", a.mutation(a.annotations))
	mux.HandleFunc("GET /api/albums", wrap(a.albums))
	mux.HandleFunc("POST /api/albums", a.mutation(a.createAlbum))
	mux.HandleFunc("POST /api/albums/{album}", a.mutation(a.editAlbum))
	mux.HandleFunc("GET /api/collections", wrap(a.collections))
	mux.HandleFunc("POST /api/collections", a.mutation(a.editCollection))
}

type annotation struct {
	Favorite     bool     `json:"favorite"`
	Hidden       bool     `json:"hidden"`
	Title        string   `json:"title"`
	Description  string   `json:"description"`
	Location     string   `json:"location"`
	AutoLocation string   `json:"auto_location"`
	Tags         []string `json:"tags"`
	Albums       []int64  `json:"albums"`
}

func (a *AI) annotation(id string) (annotation, error) {
	p := annotation{Tags: []string{}, Albums: []int64{}}
	var tags string
	err := a.read.QueryRow(`SELECT favorite,hidden,title,description,location,tags FROM curation.annotations WHERE root=? AND image_id=?`, a.lib.rootPath, id).Scan(&p.Favorite, &p.Hidden, &p.Title, &p.Description, &p.Location, &tags)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return p, err
	}
	if err = a.read.QueryRow(`SELECT g.label FROM catalog.locations g JOIN catalog.images i ON i.id=g.image_id AND i.signature=g.signature WHERE g.image_id=?`, id).Scan(&p.AutoLocation); err != nil && !errors.Is(err, sql.ErrNoRows) {
		return p, err
	}
	if tags != "" {
		if err = json.Unmarshal([]byte(tags), &p.Tags); err != nil {
			return p, err
		}
	}
	rows, err := a.read.Query(`SELECT m.album_id FROM curation.album_images m JOIN curation.albums a ON a.id=m.album_id WHERE a.root=? AND m.image_id=?`, a.lib.rootPath, id)
	if err != nil {
		return p, err
	}
	defer rows.Close()
	for rows.Next() {
		var id int64
		if err = rows.Scan(&id); err != nil {
			return p, err
		}
		p.Albums = append(p.Albums, id)
	}
	return p, rows.Err()
}
func (a *AI) getAnnotation(w http.ResponseWriter, r *http.Request) error {
	p, err := a.annotation(r.PathValue("id"))
	if err != nil {
		return err
	}
	return writeJSON(w, 200, p)
}
func (a *AI) annotations(w http.ResponseWriter, r *http.Request) error {
	var body struct {
		IDs []string `json:"ids"`
	}
	if json.NewDecoder(r.Body).Decode(&body) != nil || len(body.IDs) > 60 {
		return bad(400, "Provide up to 60 image IDs")
	}
	result := map[string]annotation{}
	for _, id := range body.IDs {
		p, err := a.annotation(id)
		if err != nil {
			return err
		}
		result[id] = p
	}
	return writeJSON(w, 200, result)
}
func (a *AI) editAnnotation(w http.ResponseWriter, r *http.Request) error {
	id := r.PathValue("id")
	if _, err := a.lib.get(r.Context(), id); err != nil {
		return err
	}
	var body struct {
		Favorite    *bool     `json:"favorite"`
		Hidden      *bool     `json:"hidden"`
		Title       *string   `json:"title"`
		Description *string   `json:"description"`
		Location    *string   `json:"location"`
		Tags        *[]string `json:"tags"`
	}
	if json.NewDecoder(r.Body).Decode(&body) != nil {
		return bad(400, "Invalid JSON")
	}
	// Read/modify/write under one transaction so independent edits don't overwrite each other.
	tx, err := a.db.BeginTx(r.Context(), nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	p := annotation{Tags: []string{}}
	tags := "[]"
	err = tx.QueryRow(`SELECT favorite,hidden,title,description,location,tags FROM curation.annotations WHERE root=? AND image_id=?`, a.lib.rootPath, id).Scan(&p.Favorite, &p.Hidden, &p.Title, &p.Description, &p.Location, &tags)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return err
	}
	if body.Favorite != nil {
		p.Favorite = *body.Favorite
	}
	if body.Hidden != nil {
		p.Hidden = *body.Hidden
	}
	for _, field := range []struct {
		value *string
		dest  *string
		limit int
	}{{body.Title, &p.Title, 150}, {body.Description, &p.Description, 1000}, {body.Location, &p.Location, 150}} {
		if field.value != nil {
			value := strings.TrimSpace(*field.value)
			if utf8.RuneCountInString(value) > field.limit {
				return bad(400, "Text is too long")
			}
			*field.dest = value
		}
	}
	if body.Tags != nil {
		if len(*body.Tags) > 20 {
			return bad(400, "Use at most 20 tags")
		}
		clean := []string{}
		seen := map[string]bool{}
		for _, tag := range *body.Tags {
			tag = strings.TrimSpace(tag)
			if utf8.RuneCountInString(tag) > 50 {
				return bad(400, "Tags must be at most 50 characters")
			}
			if tag != "" && !seen[strings.ToLower(tag)] {
				clean = append(clean, tag)
				seen[strings.ToLower(tag)] = true
			}
		}
		raw, _ := json.Marshal(clean)
		tags = string(raw)
	}
	_, err = tx.Exec(`INSERT INTO curation.annotations(root,image_id,favorite,hidden,title,description,location,tags,updated_at) VALUES(?,?,?,?,?,?,?,?,?) ON CONFLICT(root,image_id) DO UPDATE SET favorite=excluded.favorite,hidden=excluded.hidden,title=excluded.title,description=excluded.description,location=excluded.location,tags=excluded.tags,updated_at=excluded.updated_at`, a.lib.rootPath, id, p.Favorite, p.Hidden, p.Title, p.Description, p.Location, tags, time.Now().UTC().Format(time.RFC3339))
	if err != nil {
		return err
	}
	if err = tx.Commit(); err != nil {
		return err
	}
	return a.getAnnotation(w, r)
}

func (l *Library) curationSelection(r *http.Request, clause string, args []any) (string, []any, error) {
	q := r.URL.Query()
	root := l.rootPath
	hidden := q.Get("hidden")
	switch hidden {
	case "", "false":
		clause += ` AND id NOT IN(SELECT image_id FROM curation.annotations WHERE root=? AND hidden=1)`
		args = append(args, root)
	case "only":
		clause += ` AND id IN(SELECT image_id FROM curation.annotations WHERE root=? AND hidden=1)`
		args = append(args, root)
	case "all":
	default:
		return "", nil, bad(400, "Invalid hidden filter")
	}
	if q.Get("favorite") == "true" {
		clause += ` AND id IN(SELECT image_id FROM curation.annotations WHERE root=? AND favorite=1)`
		args = append(args, root)
	}
	if album := q.Get("album"); album != "" {
		id, err := strconv.ParseInt(album, 10, 64)
		if err != nil || id < 1 {
			return "", nil, bad(400, "Invalid album")
		}
		clause += ` AND id IN(SELECT m.image_id FROM curation.album_images m JOIN curation.albums c ON c.id=m.album_id WHERE c.root=? AND c.id=?)`
		args = append(args, root, id)
	}
	dateExpr := `replace(substr(coalesce(taken_at,created_at,''),1,10),':','-')`
	for _, key := range []string{"from", "to"} {
		if value := q.Get(key); value != "" {
			if _, err := time.Parse("2006-01-02", value); err != nil {
				return "", nil, bad(400, "Dates must use YYYY-MM-DD")
			}
			op := ">="
			if key == "to" {
				op = "<="
			}
			clause += " AND " + dateExpr + op + "?"
			args = append(args, value)
		}
	}
	if q.Get("from") != "" && q.Get("to") != "" && q.Get("from") > q.Get("to") {
		return "", nil, bad(400, "Start date must precede end date")
	}
	if q.Get("on_this_day") == "true" {
		clause += " AND substr(" + dateExpr + ",6,5)=?"
		args = append(args, time.Now().Format("01-02"))
	}
	if place := strings.TrimSpace(q.Get("place")); place != "" {
		if utf8.RuneCountInString(place) > 150 {
			return "", nil, bad(400, "Place is too long")
		}
		clause += ` AND id IN(SELECT image_id FROM curation.annotations WHERE root=? AND location LIKE ? ESCAPE '!' UNION SELECT g.image_id FROM locations g JOIN images current ON current.id=g.image_id AND current.signature=g.signature WHERE g.label LIKE ? ESCAPE '!' AND g.image_id NOT IN(SELECT image_id FROM curation.annotations WHERE root=? AND location!=''))`
		args = append(args, root, "%"+escapeLike(place)+"%", "%"+escapeLike(place)+"%", root)
	}
	if tag := q.Get("tag"); tag != "" {
		clause += ` AND id IN(SELECT c.image_id FROM curation.annotations c,json_each(c.tags) WHERE c.root=? AND json_each.value=? COLLATE NOCASE)`
		args = append(args, root, tag)
	}
	return clause, args, nil
}

func validName(name string) bool {
	return strings.TrimSpace(name) != "" && utf8.RuneCountInString(name) <= 100
}
func (a *AI) albums(w http.ResponseWriter, r *http.Request) error {
	rows, err := a.read.QueryContext(r.Context(), `SELECT a.id,a.name,count(i.id),coalesce(min(i.id),'') FROM curation.albums a LEFT JOIN curation.album_images m ON m.album_id=a.id LEFT JOIN catalog.images i ON i.id=m.image_id AND i.id NOT IN(SELECT image_id FROM curation.annotations WHERE root=? AND hidden=1) WHERE a.root=? GROUP BY a.id ORDER BY a.name COLLATE NOCASE`, a.lib.rootPath, a.lib.rootPath)
	if err != nil {
		return err
	}
	defer rows.Close()
	items := []map[string]any{}
	for rows.Next() {
		var id int64
		var name, cover string
		var count int
		if err = rows.Scan(&id, &name, &count, &cover); err != nil {
			return err
		}
		url := ""
		if cover != "" {
			url = "/api/images/" + cover + "/thumbnail"
		}
		items = append(items, map[string]any{"id": id, "name": name, "photo_count": count, "image_url": url})
	}
	if err = rows.Err(); err != nil {
		return err
	}
	return writeJSON(w, 200, map[string]any{"items": items})
}
func (a *AI) createAlbum(w http.ResponseWriter, r *http.Request) error {
	var body struct {
		Name string `json:"name"`
	}
	if json.NewDecoder(r.Body).Decode(&body) != nil || !validName(body.Name) {
		return bad(400, "Give the album a name (up to 100 characters)")
	}
	result, err := a.db.ExecContext(r.Context(), `INSERT INTO curation.albums(root,name,created_at)VALUES(?,?,?)`, a.lib.rootPath, strings.TrimSpace(body.Name), time.Now().UTC().Format(time.RFC3339))
	if err != nil {
		return err
	}
	id, _ := result.LastInsertId()
	return writeJSON(w, 201, map[string]any{"id": id, "name": strings.TrimSpace(body.Name)})
}
func (a *AI) editAlbum(w http.ResponseWriter, r *http.Request) error {
	id, err := strconv.ParseInt(r.PathValue("album"), 10, 64)
	if err != nil || id < 1 {
		return bad(400, "Invalid album")
	}
	var body struct {
		Action  string `json:"action"`
		Name    string `json:"name"`
		ImageID string `json:"image_id"`
	}
	if json.NewDecoder(r.Body).Decode(&body) != nil {
		return bad(400, "Invalid JSON")
	}
	tx, err := a.db.BeginTx(r.Context(), nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var found int64
	if err = tx.QueryRow(`SELECT id FROM curation.albums WHERE id=? AND root=?`, id, a.lib.rootPath).Scan(&found); errors.Is(err, sql.ErrNoRows) {
		return bad(404, "Album not found")
	} else if err != nil {
		return err
	}
	switch body.Action {
	case "rename":
		if !validName(body.Name) {
			return bad(400, "Provide a name up to 100 characters")
		}
		_, err = tx.Exec(`UPDATE curation.albums SET name=? WHERE id=?`, strings.TrimSpace(body.Name), id)
	case "delete":
		if _, err = tx.Exec(`DELETE FROM curation.album_images WHERE album_id=?`, id); err == nil {
			_, err = tx.Exec(`DELETE FROM curation.albums WHERE id=?`, id)
		}
	case "add", "remove":
		var exists int
		if err = tx.QueryRow(`SELECT count(*) FROM catalog.images WHERE id=?`, body.ImageID).Scan(&exists); err != nil {
			return err
		}
		if exists == 0 {
			return bad(404, "Image not found")
		}
		if body.Action == "add" {
			_, err = tx.Exec(`INSERT OR IGNORE INTO curation.album_images VALUES(?,?)`, id, body.ImageID)
		} else {
			_, err = tx.Exec(`DELETE FROM curation.album_images WHERE album_id=? AND image_id=?`, id, body.ImageID)
		}
	default:
		return bad(400, "Choose add, remove, rename, or delete")
	}
	if err != nil {
		return err
	}
	if err = tx.Commit(); err != nil {
		return err
	}
	return writeJSON(w, 200, map[string]any{"ok": true})
}

func (a *AI) collections(w http.ResponseWriter, r *http.Request) error {
	rows, err := a.read.QueryContext(r.Context(), `SELECT id,name,query FROM curation.collections WHERE root=? ORDER BY name COLLATE NOCASE`, a.lib.rootPath)
	if err != nil {
		return err
	}
	defer rows.Close()
	items := []map[string]any{}
	for rows.Next() {
		var id int64
		var name, query string
		if err = rows.Scan(&id, &name, &query); err != nil {
			return err
		}
		items = append(items, map[string]any{"id": id, "name": name, "query": json.RawMessage(query)})
	}
	if err = rows.Err(); err != nil {
		return err
	}
	return writeJSON(w, 200, map[string]any{"items": items})
}
func (a *AI) editCollection(w http.ResponseWriter, r *http.Request) error {
	var body struct {
		ID     int64             `json:"id"`
		Action string            `json:"action"`
		Name   string            `json:"name"`
		Query  map[string]string `json:"query"`
	}
	if json.NewDecoder(r.Body).Decode(&body) != nil {
		return bad(400, "Invalid JSON")
	}
	if body.Action == "delete" {
		_, err := a.db.ExecContext(r.Context(), `DELETE FROM curation.collections WHERE id=? AND root=?`, body.ID, a.lib.rootPath)
		if err != nil {
			return err
		}
		return writeJSON(w, 200, map[string]bool{"ok": true})
	}
	if !validName(body.Name) {
		return bad(400, "Provide a name up to 100 characters")
	}
	allowed := map[string]bool{}
	for _, key := range []string{"q", "mode", "directory", "media_type", "person", "favorite", "hidden", "album", "from", "to", "place", "tag", "on_this_day", "group_by", "sort", "direction", "layout"} {
		allowed[key] = true
	}
	for key, val := range body.Query {
		if !allowed[key] || len(val) > 500 {
			return bad(400, "Invalid saved filter")
		}
	}
	raw, err := json.Marshal(body.Query)
	if err != nil {
		return err
	}
	result, err := a.db.ExecContext(r.Context(), `INSERT INTO curation.collections(root,name,query,created_at)VALUES(?,?,?,?)`, a.lib.rootPath, strings.TrimSpace(body.Name), string(raw), time.Now().UTC().Format(time.RFC3339))
	if err != nil {
		return err
	}
	id, _ := result.LastInsertId()
	return writeJSON(w, 201, map[string]int64{"id": id})
}
func (a *AI) exportCuration(w http.ResponseWriter, r *http.Request) error {
	result := map[string]any{"version": 1, "root": a.lib.rootPath, "exported_at": time.Now().UTC().Format(time.RFC3339)}
	for _, table := range []string{"annotations", "albums", "collections", "album_images"} {
		query := "SELECT * FROM curation." + table + " WHERE root=?"
		if table == "album_images" {
			query = `SELECT * FROM curation.album_images WHERE album_id IN(SELECT id FROM curation.albums WHERE root=?)`
		}
		rows, err := a.read.QueryContext(r.Context(), query, a.lib.rootPath)
		if err != nil {
			return err
		}
		columns, _ := rows.Columns()
		items := []map[string]any{}
		for rows.Next() {
			values := make([]any, len(columns))
			ptr := make([]any, len(columns))
			for i := range values {
				ptr[i] = &values[i]
			}
			if err = rows.Scan(ptr...); err != nil {
				rows.Close()
				return err
			}
			item := map[string]any{}
			for i, key := range columns {
				item[key] = values[i]
			}
			items = append(items, item)
		}
		err = rows.Err()
		rows.Close()
		if err != nil {
			return err
		}
		result[table] = items
	}
	w.Header().Set("Content-Disposition", `attachment; filename="home-images-curation.json"`)
	return writeJSON(w, 200, result)
}
