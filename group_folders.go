package main

import (
	"encoding/json"
	"net/http"
	"sort"
)

// Group cards paginate groups, not membership rows. Memberships are built from
// the complete filtered selection so a group never splits across folder pages.
func (a *AI) photoGroupFolders(w http.ResponseWriter, r *http.Request, q, mode, clause string, args []any, limit, offset int) error {
	kind := r.URL.Query().Get("group_by")
	switch kind {
	case "directory", "path", "year", "people", "tag":
	default:
		return bad(400, "Unknown photo grouping")
	}
	semantic := mode == "semantic" && q != ""
	if semantic {
		selection, err := a.cachedSelection(r, q, "semantic", clause, args, semanticGroupPool)
		if err != nil {
			return err
		}
		ids := []string{}
		for _, hit := range selection.Hits {
			ids = append(ids, hit.ID)
		}
		raw, _ := json.Marshal(ids)
		clause += " AND i.id IN(SELECT value FROM json_each(?))"
		args = append(append([]any{}, args...), string(raw))
	}
	cte := `WITH filtered AS NOT MATERIALIZED (SELECT i.id,i.signature,i.directory,i.taken_at,i.created_at FROM catalog.images i WHERE ` + clause + `), `
	fallback := ""
	switch kind {
	case "directory", "path":
		cte += `memberships AS (SELECT id image_id,directory group_key,coalesce(nullif(directory,''),'Root folder') group_label FROM filtered)`
	case "year":
		cte += `memberships AS (SELECT id image_id,coalesce(nullif(substr(coalesce(nullif(taken_at,''),created_at),1,4),''),'Unknown year') COLLATE NOCASE group_key,coalesce(nullif(substr(coalesce(nullif(taken_at,''),created_at),1,4),''),'Unknown year') COLLATE NOCASE group_label FROM filtered)`
		fallback = "Unknown year"
	case "people":
		cte += `labels AS MATERIALIZED (SELECT DISTINCT i.id image_id,CAST(p.id AS TEXT) group_key,coalesce(nullif(p.name,''),'Person '||p.id) group_label FROM filtered i JOIN faces f ON f.image_id=i.id JOIN people p ON p.id=f.person_id JOIN ai_images ai ON ai.image_id=i.id AND ai.signature=i.signature WHERE ai.models=? AND ai.error='' AND p.merged_into IS NULL), memberships AS (SELECT * FROM labels UNION ALL SELECT id,'none','No indexed people' FROM filtered i WHERE NOT EXISTS(SELECT 1 FROM labels l WHERE l.image_id=i.id))`
		args = append(args, a.models)
		fallback = "none"
	case "tag":
		cte += `labels AS MATERIALIZED (SELECT i.id image_id,lower(trim(j.value)) group_key,min(trim(j.value)) group_label FROM filtered i JOIN curation.annotations c ON c.image_id=i.id AND c.root=? JOIN json_each(c.tags) j WHERE j.type='text' AND trim(j.value)!='' GROUP BY i.id,lower(trim(j.value))), memberships AS (SELECT * FROM labels UNION ALL SELECT id,'','No tags' FROM filtered i WHERE NOT EXISTS(SELECT 1 FROM labels l WHERE l.image_id=i.id))`
		args = append(args, a.lib.rootPath)
	}
	rows, err := a.read.QueryContext(r.Context(), cte+` SELECT group_key,min(group_label),count(*),min(image_id) FROM memberships GROUP BY group_key`, args...)
	if err != nil {
		return err
	}
	defer rows.Close()
	type folder struct {
		Key   string `json:"key"`
		Label string `json:"label"`
		Count int    `json:"count"`
		Cover string `json:"cover"`
	}
	groups := []folder{}
	for rows.Next() {
		var g folder
		if err = rows.Scan(&g.Key, &g.Label, &g.Count, &g.Cover); err != nil {
			return err
		}
		groups = append(groups, g)
	}
	if err = rows.Err(); err != nil {
		return err
	}
	desc := r.URL.Query().Get("direction") == "desc"
	sort.Slice(groups, func(i, j int) bool {
		if kind != "directory" && kind != "path" && (groups[i].Key == fallback || groups[j].Key == fallback) {
			return groups[i].Key != fallback
		}
		left, right := foldGroupLabel(groups[i].Label), foldGroupLabel(groups[j].Label)
		if left == right {
			return groups[i].Key < groups[j].Key
		}
		if desc {
			return left > right
		}
		return left < right
	})
	total := len(groups)
	start := min(offset, total)
	end := min(start+limit, total)
	return writeJSON(w, 200, map[string]any{"items": groups[start:end], "total": total, "photo_groups": true, "group_by": kind, "semantic": semantic})
}
