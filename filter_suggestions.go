package main

import (
	"net/http"
	"strings"
	"unicode/utf8"
)

func (a *AI) filterSuggestions(w http.ResponseWriter, r *http.Request) error {
	q := strings.TrimSpace(r.URL.Query().Get("q"))
	if utf8.RuneCountInString(q) > 500 {
		return bad(400, "Query too long")
	}
	pattern := "%" + escapeLike(q) + "%"
	visible := `i.id NOT IN(SELECT image_id FROM curation.annotations WHERE root=? AND hidden=1)`
	var query string
	var args []any
	switch r.URL.Query().Get("kind") {
	case "folder":
		query = `SELECT DISTINCT i.directory value FROM catalog.images i WHERE ` + visible + ` AND i.directory!='' AND i.directory LIKE ? ESCAPE '!' ORDER BY value COLLATE NOCASE LIMIT 30`
		args = []any{a.lib.rootPath, pattern}
	case "tag":
		query = `SELECT min(j.value) value FROM curation.annotations c JOIN catalog.images i ON i.id=c.image_id JOIN json_each(c.tags) j WHERE c.root=? AND ` + visible + ` AND j.type='text' AND j.value LIKE ? ESCAPE '!' GROUP BY lower(j.value) ORDER BY value COLLATE NOCASE LIMIT 30`
		args = []any{a.lib.rootPath, a.lib.rootPath, pattern}
	case "place":
		query = `SELECT value FROM (SELECT c.location value FROM curation.annotations c JOIN catalog.images i ON i.id=c.image_id WHERE c.root=? AND ` + visible + ` AND c.location!='' AND c.location LIKE ? ESCAPE '!' UNION SELECT g.label value FROM catalog.locations g JOIN catalog.images i ON i.id=g.image_id AND i.signature=g.signature WHERE ` + visible + ` AND g.label!='' AND g.label LIKE ? ESCAPE '!' AND g.image_id NOT IN(SELECT image_id FROM curation.annotations WHERE root=? AND location!='')) GROUP BY value COLLATE NOCASE ORDER BY value COLLATE NOCASE LIMIT 30`
		args = []any{a.lib.rootPath, a.lib.rootPath, pattern, a.lib.rootPath, pattern, a.lib.rootPath}
	default:
		return bad(400, "Unknown filter kind")
	}
	rows, err := a.read.QueryContext(r.Context(), query, args...)
	if err != nil {
		return err
	}
	defer rows.Close()
	items := []string{}
	for rows.Next() {
		var value string
		if err = rows.Scan(&value); err != nil {
			return err
		}
		items = append(items, value)
	}
	if err = rows.Err(); err != nil {
		return err
	}
	return writeJSON(w, 200, map[string]any{"items": items})
}
