package main

import (
	"encoding/json"
	"net/http"
)

const semanticGroupPool = 1200

// Rank a bounded, page-independent pool first, then build all memberships before
// pagination. Grouping never admits photos outside the semantic result pool.
func (a *AI) semanticGroupedSearch(w http.ResponseWriter, r *http.Request, q, clause string, args []any, limit, offset int) error {
	kind := r.URL.Query().Get("group_by")
	switch kind {
	case "path", "year", "people", "tag":
	default:
		return bad(400, "Unknown photo grouping")
	}
	selection, err := a.cachedSelection(r, q, "semantic", clause, args, semanticGroupPool)
	if err != nil {
		return err
	}
	hits, err := json.Marshal(selection.Hits)
	if err != nil {
		return err
	}
	cte := `WITH ranked AS MATERIALIZED (
 SELECT json_extract(value,'$.ID') image_id,CAST(key AS INTEGER) rank,json_extract(value,'$.Score') score FROM json_each(?)
 ), filtered AS MATERIALIZED (
 SELECT i.id,i.directory,i.taken_at,i.created_at,r.rank,r.score FROM ranked r
 CROSS JOIN catalog.images i ON i.id=r.image_id
 CROSS JOIN ai_images a INDEXED BY ai_status ON a.image_id=i.id AND a.signature=i.signature
 WHERE a.models=? AND a.error='' AND (` + clause + `)), `
	queryArgs := append([]any{string(hits), a.models}, args...)
	fallback := ""
	switch kind {
	case "path":
		cte += `memberships AS MATERIALIZED (SELECT id image_id,directory group_key,coalesce(nullif(directory,''),'Root folder') group_label,rank,score FROM filtered), `
	case "year":
		cte += `memberships AS MATERIALIZED (SELECT id image_id,coalesce(nullif(substr(coalesce(nullif(taken_at,''),created_at),1,4),''),'Unknown year') group_key,coalesce(nullif(substr(coalesce(nullif(taken_at,''),created_at),1,4),''),'Unknown year') group_label,rank,score FROM filtered), `
		fallback = "Unknown year"
	case "people":
		cte += `labels AS MATERIALIZED (
 SELECT DISTINCT i.id image_id,CAST(p.id AS TEXT) group_key,coalesce(nullif(p.name,''),'Person '||p.id) group_label,i.rank,i.score
 FROM filtered i JOIN faces f ON f.image_id=i.id JOIN people p ON p.id=f.person_id WHERE p.merged_into IS NULL
 ), memberships AS MATERIALIZED (
 SELECT * FROM labels UNION ALL SELECT id,'none','No indexed people',rank,score FROM filtered i WHERE NOT EXISTS(SELECT 1 FROM labels l WHERE l.image_id=i.id)), `
		fallback = "none"
	case "tag":
		cte += `labels AS MATERIALIZED (
 SELECT i.id image_id,lower(trim(j.value)) group_key,min(trim(j.value)) group_label,i.rank,i.score
 FROM filtered i JOIN curation.annotations c ON c.image_id=i.id AND c.root=? JOIN json_each(c.tags) j
 WHERE j.type='text' AND trim(j.value)!='' GROUP BY i.id,lower(trim(j.value))
 ), memberships AS MATERIALIZED (
 SELECT * FROM labels UNION ALL SELECT id,'','No tags',rank,score FROM filtered i WHERE NOT EXISTS(SELECT 1 FROM labels l WHERE l.image_id=i.id)), `
		queryArgs = append(queryArgs, a.lib.rootPath)
	}
	cte += `groups AS MATERIALIZED (SELECT group_key,min(group_label) group_label FROM memberships GROUP BY group_key)`
	var unique, total int
	if err = a.read.QueryRowContext(r.Context(), cte+` SELECT (SELECT count(*) FROM filtered),(SELECT count(*) FROM memberships)`, queryArgs...).Scan(&unique, &total); err != nil {
		return err
	}
	direction := "ASC"
	if r.URL.Query().Get("direction") == "desc" {
		direction = "DESC"
	}
	groupOrder := "g.group_label COLLATE NOCASE " + direction + ",g.group_key,"
	if kind == "path" || kind == "year" {
		groupOrder = "g.group_key COLLATE NOCASE " + direction + ",g.group_key " + direction + ","
	}
	// Missing groups stay last in either direction. These keys are constants,
	// never user-supplied SQL.
	if kind != "path" {
		groupOrder = "CASE WHEN g.group_key='" + fallback + "' THEN 1 ELSE 0 END," + groupOrder
	}
	query := cte + `, page AS MATERIALIZED (
 SELECT m.image_id,m.group_key,g.group_label,m.score,row_number() OVER(ORDER BY ` + groupOrder + `m.rank) position
 FROM memberships m JOIN groups g ON g.group_key=m.group_key ORDER BY position LIMIT ? OFFSET ?
 ) SELECT metadata_json(i.metadata),m.group_key,m.group_label,m.score FROM page m JOIN catalog.images i ON i.id=m.image_id ORDER BY m.position`
	pageArgs := append(append([]any{}, queryArgs...), limit, offset)
	rows, err := a.read.QueryContext(r.Context(), query, pageArgs...)
	if err != nil {
		return err
	}
	defer rows.Close()
	type item struct {
		groupedPhoto
		Similarity float32 `json:"similarity"`
	}
	items := []item{}
	for rows.Next() {
		var raw string
		var p item
		if err = rows.Scan(&raw, &p.GroupKey, &p.GroupLabel, &p.Similarity); err != nil {
			return err
		}
		if err = json.Unmarshal([]byte(raw), &p.Photo); err != nil {
			return err
		}
		items = append(items, p)
	}
	if err = rows.Err(); err != nil {
		return err
	}
	return writeJSON(w, 200, map[string]any{"items": items, "total": total, "unique_total": unique, "offset": offset, "semantic": true, "group_by": kind, "indexed_candidates": selection.Total, "ranked_limit": semanticGroupPool})
}
