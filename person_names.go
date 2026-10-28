package main

import (
	"net/http"
	"strings"
	"unicode/utf8"
)

// Autocomplete needs names, not photo counts or capture-year aggregates. Start
// with named people and stop at their first current, visible face.
func (a *AI) personNames(w http.ResponseWriter, r *http.Request) error {
	limit, err := intParam(r, "limit", 50, 1, 200)
	if err != nil {
		return err
	}
	q := strings.TrimSpace(r.URL.Query().Get("q"))
	if utf8.RuneCountInString(q) > 100 {
		return bad(400, "Name query too long")
	}
	visible, args, err := a.lib.curationSelection(r, "1=1", nil)
	if err != nil {
		return err
	}
	queryArgs := []any{a.lib.config.ML.FaceModel, "%" + escapeLike(q) + "%", a.models}
	queryArgs = append(queryArgs, args...)
	queryArgs = append(queryArgs, limit)
	rows, err := a.read.QueryContext(r.Context(), `SELECT p.id,p.name
 FROM people p INDEXED BY people_name
 WHERE p.model=? AND p.merged_into IS NULL AND p.name!='' AND p.name LIKE ? ESCAPE '!'
 AND EXISTS(SELECT 1 FROM faces f INDEXED BY face_person
 CROSS JOIN ai_images a ON a.image_id=f.image_id
 CROSS JOIN catalog.images i ON i.id=a.image_id AND i.signature=a.signature
 WHERE f.person_id=p.id AND a.models=? AND a.error=''
 AND i.id IN(SELECT id FROM catalog.images WHERE id=f.image_id AND `+visible+`))
 ORDER BY p.name COLLATE NOCASE,p.id LIMIT ?`, queryArgs...)
	if err != nil {
		return err
	}
	defer rows.Close()
	type nameMatch struct {
		ID   int64  `json:"id"`
		Name string `json:"name"`
	}
	items := []nameMatch{}
	for rows.Next() {
		var p nameMatch
		if err := rows.Scan(&p.ID, &p.Name); err != nil {
			return err
		}
		items = append(items, p)
	}
	if err := rows.Err(); err != nil {
		return err
	}
	return writeJSON(w, 200, map[string]any{"items": items})
}
