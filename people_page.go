package main

import (
	"database/sql"
	"fmt"
	"net/http"
)

// Validate each indexed photo once, rather than revisiting the catalog and
// curation filter for every face. Only the requested page needs capture years.
// Name searches keep the selective person-first query in people().
func (a *AI) peoplePage(w http.ResponseWriter, r *http.Request, limit, offset int) error {
	visible, args, err := a.lib.curationSelection(r, "1=1", nil)
	if err != nil {
		return err
	}
	queryArgs := append(args, a.models, limit, offset)
	rows, err := a.read.QueryContext(r.Context(), `WITH eligible AS MATERIALIZED (
 SELECT a.image_id,i.taken_at FROM ai_images a
 JOIN (SELECT id,signature,taken_at FROM catalog.images INDEXED BY image_person_capture WHERE `+visible+`) i
 ON i.id=a.image_id AND i.signature=a.signature
 WHERE a.models=? AND a.error=''
), groups AS MATERIALIZED (
 SELECT p.id,p.name,count(DISTINCT f.image_id) AS photo_count
 FROM people p JOIN faces f ON f.person_id=p.id JOIN eligible i ON i.image_id=f.image_id
 WHERE p.merged_into IS NULL GROUP BY p.id
), page AS MATERIALIZED (
 SELECT * FROM groups ORDER BY photo_count DESC,id LIMIT ? OFFSET ?
)
 SELECT page.id,page.name,page.photo_count`+personYearRangeSQL+`,total.n
 FROM (SELECT count(*) AS n FROM groups) total
 LEFT JOIN page ON 1
 LEFT JOIN faces f ON f.person_id=page.id
 LEFT JOIN eligible i ON i.image_id=f.image_id
 GROUP BY page.id ORDER BY page.photo_count DESC,page.id`, queryArgs...)
	if err != nil {
		return err
	}
	defer rows.Close()
	items := []personSummary{}
	total := 0
	for rows.Next() {
		var id, count sql.NullInt64
		var name sql.NullString
		var p personSummary
		if err := rows.Scan(&id, &name, &count, &p.FirstYear, &p.LastYear, &total); err != nil {
			return err
		}
		// The total's LEFT JOIN returns one empty row past the last page.
		if !id.Valid {
			continue
		}
		p.ID, p.Name, p.Count = id.Int64, name.String, int(count.Int64)
		p.ImageURL = fmt.Sprintf("/api/people/%d/image", p.ID)
		if hidden := r.URL.Query().Get("hidden"); hidden == "all" || hidden == "only" {
			p.ImageURL += "?hidden=" + hidden
		}
		items = append(items, p)
	}
	if err := rows.Err(); err != nil {
		return err
	}
	return writeJSON(w, 200, map[string]any{"items": items, "total": total, "offset": offset})
}
