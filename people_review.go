package main

import (
	"database/sql"
	"encoding/json"
	"errors"
	"net/http"
	"sort"
	"strconv"
)

type personSuggestion struct {
	personSummary
	Similarity float32 `json:"similarity"`
}

// Suggestions use all retained group representatives, including merged aliases.
// They never change assignments without explicit confirmation.
func (a *AI) personSuggestions(w http.ResponseWriter, r *http.Request) error {
	id, err := personID(r)
	if err != nil {
		return err
	}
	rows, err := a.read.QueryContext(r.Context(), `SELECT coalesce(merged_into,id),embedding FROM people WHERE model=?`, a.lib.config.ML.FaceModel)
	if err != nil {
		return err
	}
	vectors := map[int64][]vector{}
	for rows.Next() {
		var key int64
		var blob []byte
		if err = rows.Scan(&key, &blob); err != nil {
			rows.Close()
			return err
		}
		vectors[key] = append(vectors[key], vectorBlob(blob))
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return err
	}
	if len(vectors[id]) == 0 {
		return bad(404, "Person not found")
	}
	visible, visibilityArgs, err := a.lib.curationSelection(r, "1=1", nil)
	if err != nil {
		return err
	}
	args := []any{id, a.models, id, id, id}
	args = append(args, visibilityArgs...)
	rows, err = a.read.QueryContext(r.Context(), `SELECT p.id,p.name,count(DISTINCT f.image_id) FROM people p JOIN faces f ON f.person_id=p.id JOIN ai_images ai ON ai.image_id=f.image_id JOIN catalog.images i ON i.id=ai.image_id AND i.signature=ai.signature WHERE p.merged_into IS NULL AND p.id!=? AND ai.models=? AND ai.error='' AND NOT EXISTS(SELECT 1 FROM person_dismissals d WHERE (d.a=? AND d.b=p.id) OR (d.b=? AND d.a=p.id)) AND NOT EXISTS(SELECT 1 FROM faces x JOIN faces y ON x.image_id=y.image_id WHERE x.person_id=? AND y.person_id=p.id) AND i.id IN(SELECT id FROM catalog.images WHERE `+visible+`) GROUP BY p.id`, args...)
	if err != nil {
		return err
	}
	defer rows.Close()
	items := []personSuggestion{}
	for rows.Next() {
		var p personSuggestion
		if err = rows.Scan(&p.ID, &p.Name, &p.Count); err != nil {
			return err
		}
		for _, v := range vectors[id] {
			for _, other := range vectors[p.ID] {
				if score := similarity(v, other); score > p.Similarity {
					p.Similarity = score
				}
			}
		}
		if p.Similarity < 0.3 {
			continue
		}
		p.ImageURL = "/api/people/" + strconv.FormatInt(p.ID, 10) + "/image"
		items = append(items, p)
	}
	if err = rows.Err(); err != nil {
		return err
	}
	sort.Slice(items, func(i, j int) bool {
		if items[i].Similarity == items[j].Similarity {
			return items[i].ID < items[j].ID
		}
		return items[i].Similarity > items[j].Similarity
	})
	if len(items) > 12 {
		items = items[:12]
	}
	return writeJSON(w, 200, map[string]any{"items": items})
}

func (a *AI) dismissPerson(w http.ResponseWriter, r *http.Request) error {
	id, err := personID(r)
	if err != nil {
		return err
	}
	var body struct {
		Other int64 `json:"other"`
	}
	if json.NewDecoder(r.Body).Decode(&body) != nil || body.Other <= 0 || body.Other == id {
		return bad(400, "Choose another person")
	}
	first, second := id, body.Other
	if first > second {
		first, second = second, first
	}
	tx, err := a.db.BeginTx(r.Context(), nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var count int
	if err = tx.QueryRow("SELECT count(*) FROM people WHERE id IN (?,?) AND merged_into IS NULL", first, second).Scan(&count); err != nil {
		return err
	}
	if count != 2 {
		return bad(404, "Person not found")
	}
	if _, err = tx.Exec("INSERT OR IGNORE INTO person_dismissals(a,b) VALUES(?,?)", first, second); err != nil {
		return err
	}
	if _, err = tx.Exec("DELETE FROM ai_settings WHERE key='merge_undo'"); err != nil {
		return err
	}
	if err = tx.Commit(); err != nil {
		return err
	}
	return writeJSON(w, 200, map[string]any{"ok": true})
}

// Only the latest merge can be undone. Faces indexed afterwards stay with their
// current group; the snapshot never guesses which source they belonged to.
type mergeFace struct {
	ID    int64
	Image string
	Box   string
}
type mergeSnapshot struct {
	Previous   *mergeSnapshot `json:"previous,omitempty"`
	Dismissals [][2]int64     `json:"dismissals"`
	Source     int64          `json:"source"`
	Target     int64          `json:"target"`
	Name       string         `json:"name"`
	Faces      []mergeFace    `json:"faces"`
	Aliases    []int64        `json:"aliases"`
}

func rememberMerge(tx *sql.Tx, source, target int64) error {
	snap := mergeSnapshot{Source: source, Target: target}
	if err := tx.QueryRow("SELECT name FROM people WHERE id=?", source).Scan(&snap.Name); err != nil {
		return err
	}
	collect := func(query string, dest *[]int64) error {
		rows, err := tx.Query(query, source)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var id int64
			if err = rows.Scan(&id); err != nil {
				return err
			}
			*dest = append(*dest, id)
		}
		return rows.Err()
	}
	rows, err := tx.Query("SELECT id,image_id,box FROM faces WHERE person_id=?", source)
	if err != nil {
		return err
	}
	for rows.Next() {
		var f mergeFace
		if err = rows.Scan(&f.ID, &f.Image, &f.Box); err != nil {
			rows.Close()
			return err
		}
		snap.Faces = append(snap.Faces, f)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return err
	}
	rows, err = tx.Query("SELECT a,b FROM person_dismissals")
	if err != nil {
		return err
	}
	for rows.Next() {
		var pair [2]int64
		if err = rows.Scan(&pair[0], &pair[1]); err != nil {
			rows.Close()
			return err
		}
		snap.Dismissals = append(snap.Dismissals, pair)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return err
	}
	if err := collect("SELECT id FROM people WHERE merged_into=?", &snap.Aliases); err != nil {
		return err
	}
	blob, err := json.Marshal(snap)
	if err != nil {
		return err
	}
	_, err = tx.Exec("INSERT OR REPLACE INTO ai_settings(key,value) VALUES('merge_undo',?)", string(blob))
	return err
}
func (a *AI) personMergeHistory(w http.ResponseWriter, r *http.Request) error {
	var raw string
	err := a.read.QueryRowContext(r.Context(), "SELECT value FROM ai_settings WHERE key='merge_undo'").Scan(&raw)
	if errors.Is(err, sql.ErrNoRows) {
		return writeJSON(w, 200, map[string]any{"available": false})
	}
	if err != nil {
		return err
	}
	var snap mergeSnapshot
	if err = json.Unmarshal([]byte(raw), &snap); err != nil {
		return err
	}
	return writeJSON(w, 200, map[string]any{"available": true, "source": snap.Source, "target": snap.Target})
}
func (a *AI) undoPersonMerge(w http.ResponseWriter, r *http.Request) error {
	var body struct {
		Source int64 `json:"source"`
		Target int64 `json:"target"`
	}
	if json.NewDecoder(r.Body).Decode(&body) != nil {
		return bad(400, "Invalid JSON")
	}
	tx, err := a.db.BeginTx(r.Context(), nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var raw string
	if err = tx.QueryRow("SELECT value FROM ai_settings WHERE key='merge_undo'").Scan(&raw); errors.Is(err, sql.ErrNoRows) {
		return bad(409, "No merge to undo")
	} else if err != nil {
		return err
	}
	var snap mergeSnapshot
	if err = json.Unmarshal([]byte(raw), &snap); err != nil {
		return err
	}
	if body.Source != snap.Source || body.Target != snap.Target {
		return bad(409, "Another merge has replaced this undo")
	}
	for _, f := range snap.Faces {
		if _, err = tx.Exec("UPDATE faces SET person_id=? WHERE id=? AND person_id=? AND image_id=? AND box=?", snap.Source, f.ID, snap.Target, f.Image, f.Box); err != nil {
			return err
		}
	}
	for _, id := range snap.Aliases {
		if _, err = tx.Exec("UPDATE people SET merged_into=? WHERE id=? AND merged_into=?", snap.Source, id, snap.Target); err != nil {
			return err
		}
	}
	if _, err = tx.Exec("UPDATE people SET merged_into=NULL,name=? WHERE id=? AND merged_into=?", snap.Name, snap.Source, snap.Target); err != nil {
		return err
	}
	if _, err = tx.Exec("DELETE FROM person_dismissals"); err != nil {
		return err
	}
	for _, pair := range snap.Dismissals {
		if _, err = tx.Exec("INSERT INTO person_dismissals(a,b) VALUES(?,?)", pair[0], pair[1]); err != nil {
			return err
		}
	}
	if snap.Previous != nil {
		previous, e := json.Marshal(snap.Previous)
		if e != nil {
			return e
		}
		if _, err = tx.Exec("UPDATE ai_settings SET value=? WHERE key='merge_undo'", string(previous)); err != nil {
			return err
		}
	} else {
		if _, err = tx.Exec("DELETE FROM ai_settings WHERE key='merge_undo'"); err != nil {
			return err
		}
	}
	if err = tx.Commit(); err != nil {
		return err
	}
	return writeJSON(w, 200, map[string]any{"ok": true})
}
func (a *AI) moveFace(w http.ResponseWriter, r *http.Request) error {
	id, err := strconv.ParseInt(r.PathValue("face"), 10, 64)
	if err != nil || id <= 0 {
		return bad(400, "Invalid face")
	}
	var body struct {
		Person int64 `json:"person"`
	}
	if json.NewDecoder(r.Body).Decode(&body) != nil || body.Person <= 0 {
		return bad(400, "Choose a person")
	}
	tx, err := a.db.BeginTx(r.Context(), nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var source int64
	var model string
	if err = tx.QueryRow("SELECT f.person_id,p.model FROM faces f JOIN people p ON p.id=f.person_id WHERE f.id=?", id).Scan(&source, &model); errors.Is(err, sql.ErrNoRows) {
		return bad(404, "Face not found")
	} else if err != nil {
		return err
	}
	var targetModel string
	if err = tx.QueryRow("SELECT model FROM people WHERE id=? AND merged_into IS NULL", body.Person).Scan(&targetModel); errors.Is(err, sql.ErrNoRows) {
		return bad(404, "Person not found")
	} else if err != nil {
		return err
	}
	if model != targetModel {
		return bad(400, "Face models must match")
	}
	if _, err = tx.Exec(`INSERT OR REPLACE INTO face_overrides(image_id,signature,model,box,person_id) SELECT f.image_id,ai.signature,?,f.box,? FROM faces f JOIN ai_images ai ON ai.image_id=f.image_id WHERE f.id=?`, model, body.Person, id); err != nil {
		return err
	}
	if _, err = tx.Exec("UPDATE faces SET person_id=? WHERE id=?", body.Person, id); err != nil {
		return err
	}
	if _, err = tx.Exec("DELETE FROM ai_settings WHERE key='merge_undo'"); err != nil {
		return err
	}
	if err = tx.Commit(); err != nil {
		return err
	}
	return writeJSON(w, 200, map[string]any{"ok": true})
}
