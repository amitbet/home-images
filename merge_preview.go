package main

import (
	"net/http"
	"strconv"
	"strings"
)

// Count unique visible photos, rather than summing overlapping group counts.
func (a *AI) mergePreview(w http.ResponseWriter, r *http.Request) error {
	target, err := personID(r)
	if err != nil {
		return err
	}
	parts := strings.Split(r.URL.Query().Get("sources"), ",")
	if len(parts) > 500 {
		return bad(400, "Select at most 500 groups")
	}
	ids := []any{}
	seen := map[int64]bool{}
	for _, part := range parts {
		id, e := strconv.ParseInt(part, 10, 64)
		if e != nil || id < 1 || id == target || seen[id] {
			return bad(400, "Choose distinct source groups")
		}
		seen[id] = true
		ids = append(ids, id)
	}
	visible, args, err := a.lib.curationSelection(r, "1=1", nil)
	if err != nil {
		return err
	}
	query := `SELECT count(DISTINCT f.image_id) FROM faces f JOIN people p ON p.id=f.person_id JOIN ai_images ai ON ai.image_id=f.image_id JOIN catalog.images i ON i.id=ai.image_id AND i.signature=ai.signature WHERE p.merged_into IS NULL AND f.person_id IN(?` + strings.Repeat(",?", len(ids)-1) + `) AND ai.models=? AND ai.error='' AND i.id IN(SELECT id FROM catalog.images WHERE id=f.image_id AND ` + visible + `)`
	ids = append(ids, a.models)
	ids = append(ids, args...)
	var count int
	if err = a.read.QueryRowContext(r.Context(), query, ids...).Scan(&count); err != nil {
		return err
	}
	return writeJSON(w, 200, map[string]any{"groups": len(parts), "images": count})
}
