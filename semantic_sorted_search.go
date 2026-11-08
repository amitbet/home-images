package main

import (
	"encoding/json"
	"net/http"
)

// Sort the best semantic matches before pagination, never just the current page.
func (a *AI) sortedSemanticSearch(w http.ResponseWriter, r *http.Request, selection playSelection, order string, sortArgs []any, limit, offset int) error {
	hits, err := json.Marshal(selection.Hits)
	if err != nil {
		return err
	}
	query := `WITH ranked AS MATERIALIZED (
 SELECT json_extract(value,'$.ID') image_id,json_extract(value,'$.Score') score FROM json_each(?)
 ), page AS MATERIALIZED (
 SELECT i.id,r.score,row_number() OVER(ORDER BY ` + order + `) position
 FROM ranked r CROSS JOIN catalog.images i ON i.id=r.image_id
 ORDER BY position LIMIT ? OFFSET ?
 ) SELECT metadata_json(i.metadata),p.score FROM page p JOIN catalog.images i ON i.id=p.id ORDER BY p.position`
	args := append([]any{string(hits)}, sortArgs...)
	args = append(args, limit, offset)
	rows, err := a.read.QueryContext(r.Context(), query, args...)
	if err != nil {
		return err
	}
	defer rows.Close()
	items := []map[string]any{}
	for rows.Next() {
		var raw string
		var score float32
		if err = rows.Scan(&raw, &score); err != nil {
			return err
		}
		var item map[string]any
		if err = json.Unmarshal([]byte(raw), &item); err != nil {
			return err
		}
		item["similarity"] = score
		items = append(items, item)
	}
	if err = rows.Err(); err != nil {
		return err
	}
	return writeJSON(w, 200, map[string]any{"items": items, "total": len(selection.Hits), "indexed_candidates": selection.Total, "offset": offset, "semantic": true})
}
