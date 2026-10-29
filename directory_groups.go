package main

import (
	"encoding/json"
	"net/http"
)

func (a *AI) directoryGroups(w http.ResponseWriter, r *http.Request, q, mode, clause string, args []any, limit, offset int) error {
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
		args = append(args, string(raw))
	}
	direction := "ASC"
	if r.URL.Query().Get("direction") == "desc" {
		direction = "DESC"
	}
	rows, err := a.read.QueryContext(r.Context(), "SELECT i.directory,count(*),min(i.id) FROM catalog.images i WHERE "+clause+" GROUP BY i.directory ORDER BY i.directory COLLATE NOCASE "+direction+",i.directory", args...)
	if err != nil {
		return err
	}
	defer rows.Close()
	type group struct {
		Key   string `json:"key"`
		Count int    `json:"count"`
		Cover string `json:"cover"`
	}
	groups := []group{}
	for rows.Next() {
		var g group
		if err = rows.Scan(&g.Key, &g.Count, &g.Cover); err != nil {
			return err
		}
		groups = append(groups, g)
	}
	if err = rows.Err(); err != nil {
		return err
	}
	total := len(groups)
	start := min(offset, total)
	end := min(start+limit, total)
	return writeJSON(w, 200, map[string]any{"items": groups[start:end], "total": total, "directory_groups": true, "semantic": semantic})
}
