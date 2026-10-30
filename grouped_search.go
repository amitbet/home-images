package main

import (
	"encoding/json"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"time"
)

type groupedPhoto struct {
	Photo
	GroupKey   string `json:"group_key"`
	GroupLabel string `json:"group_label"`
}

type photoGroup struct {
	key, label        string
	count, skip, take int
}
type photoGroupSummary struct {
	groups        []photoGroup
	unique, total int
	created       time.Time
}

// Build memberships before pagination. A photo can belong to several people/tags.
func (a *AI) groupedSearch(w http.ResponseWriter, r *http.Request, clause string, args []any, order string, sortArgs []any, limit, offset int) error {
	kind := r.URL.Query().Get("group_by")
	if kind == "path" || kind == "year" || kind == "month" {
		return a.simpleGroupedSearch(w, r, clause, args, order, sortArgs, limit, offset, kind)
	}
	if kind == "tag" {
		var tagged bool
		if err := a.read.QueryRowContext(r.Context(), "SELECT EXISTS(SELECT 1 FROM curation.annotations c,json_each(c.tags) j WHERE c.root=? AND j.type='text' AND trim(j.value)!='' LIMIT 1)", a.lib.rootPath).Scan(&tagged); err != nil {
			return err
		}
		if !tagged {
			return a.simpleGroupedSearch(w, r, clause, args, order, sortArgs, limit, offset, "empty_tag")
		}
	}
	filtered := `WITH filtered AS NOT MATERIALIZED (SELECT i.id,i.signature,i.directory,i.path,i.taken_at,i.created_at,i.size_bytes FROM catalog.images i WHERE ` + clause + `), `
	memberArgs := append([]any{}, args...)
	switch kind {
	case "people":
		filtered += `labels AS MATERIALIZED (SELECT DISTINCT f.image_id,CAST(p.id AS TEXT) group_key,coalesce(nullif(p.name,''),'Person '||p.id) group_label FROM filtered i JOIN faces f ON f.image_id=i.id JOIN people p ON p.id=f.person_id JOIN ai_images ai ON ai.image_id=i.id AND ai.signature=i.signature WHERE ai.models=? AND ai.error='' AND p.merged_into IS NULL), `
		memberArgs = append(memberArgs, a.models)
	case "tag":
		filtered += `labels AS MATERIALIZED (SELECT i.id image_id,lower(trim(j.value)) group_key,min(trim(j.value)) group_label FROM filtered i JOIN curation.annotations c ON c.image_id=i.id AND c.root=? JOIN json_each(c.tags) j WHERE j.type='text' AND trim(j.value)!='' GROUP BY i.id,lower(trim(j.value))), `
		memberArgs = append(memberArgs, a.lib.rootPath)
	default:
		return bad(400, "Unknown photo grouping")
	}
	// Count only indexed/labeled memberships. Avoid materializing every
	// unassigned photo when the library is still being indexed.
	cte := strings.TrimSuffix(filtered, ", ")
	var unique, total int
	var err error
	groups := []photoGroup{}
	fallbackKey, fallbackLabel := "none", "No indexed people"
	if kind == "tag" {
		fallbackKey = ""
		fallbackLabel = "No tags"
	}
	filters := r.URL.Query()
	for _, key := range []string{"offset", "limit", "sort", "direction", "layout", "refresh"} {
		filters.Del(key)
	}
	cacheKey := filters.Encode() + "|" + strconv.FormatUint(a.selectionRevision.Load(), 10)
	a.mu.Lock()
	cached, ok := a.groupCache[cacheKey]
	a.mu.Unlock()
	if ok && time.Since(cached.created) < 15*time.Second && r.URL.Query().Get("refresh") != "1" {
		unique, total, groups = cached.unique, cached.total, cached.groups
	} else {
		if err := a.read.QueryRowContext(r.Context(), "SELECT count(*) FROM catalog.images i WHERE "+clause, args...).Scan(&unique); err != nil {
			return err
		}
		labeledUnique := 0
		rows, err := a.read.QueryContext(r.Context(), cte+` SELECT group_key,min(group_label),count(*),(SELECT count(DISTINCT image_id) FROM labels) FROM labels GROUP BY group_key`, memberArgs...)
		if err != nil {
			return err
		}
		for rows.Next() {
			var g photoGroup
			if err = rows.Scan(&g.key, &g.label, &g.count, &labeledUnique); err != nil {
				break
			}
			groups = append(groups, g)
			total += g.count
		}
		if err == nil {
			err = rows.Err()
		}
		rows.Close()
		if err != nil {
			return err
		}
		if n := unique - labeledUnique; n > 0 {
			groups = append(groups, photoGroup{key: fallbackKey, label: fallbackLabel, count: n})
			total += n
		}
		sort.Slice(groups, func(i, j int) bool {
			a, b := foldGroupLabel(groups[i].label), foldGroupLabel(groups[j].label)
			if a == b {
				return groups[i].key < groups[j].key
			}
			return a < b
		})
		a.mu.Lock()
		if a.groupCache == nil || len(a.groupCache) >= 8 {
			a.groupCache = map[string]photoGroupSummary{}
		}
		a.groupCache[cacheKey] = photoGroupSummary{groups: groups, unique: unique, total: total, created: time.Now()}
		a.mu.Unlock()
	}
	// Cache membership counts, not a direction-specific slice. Copy before sorting
	// so concurrent requests and direction changes cannot mutate cached order.
	groups = append([]photoGroup(nil), groups...)
	descending := r.URL.Query().Get("direction") == "desc"
	sort.Slice(groups, func(i, j int) bool {
		if groups[i].key == fallbackKey || groups[j].key == fallbackKey {
			return groups[i].key != fallbackKey && groups[j].key == fallbackKey
		}
		left, right := foldGroupLabel(groups[i].label), foldGroupLabel(groups[j].label)
		if left == right {
			return groups[i].key < groups[j].key
		}
		if descending {
			return left > right
		}
		return left < right
	})
	selected := []photoGroup{}
	skip, remaining := offset, limit
	for _, g := range groups {
		if skip >= g.count {
			skip -= g.count
			continue
		}
		g.skip = skip
		g.take = min(remaining, g.count-skip)
		selected = append(selected, g)
		skip = 0
		remaining -= g.take
		if remaining == 0 {
			break
		}
	}
	itemsByGroup := map[string][]groupedPhoto{}
	normalKeys := []string{}
	normalTake, normalSkip := 0, 0
	for _, g := range selected {
		if g.key != fallbackKey {
			if len(normalKeys) == 0 {
				normalSkip = g.skip
			}
			normalKeys = append(normalKeys, g.key)
			normalTake += g.take
		}
	}
	readPage := func(query string, queryArgs []any, labelMap map[string]string) error {
		rows, e := a.read.QueryContext(r.Context(), query, queryArgs...)
		if e != nil {
			return e
		}
		defer rows.Close()
		for rows.Next() {
			var raw, key string
			var p groupedPhoto
			if e = rows.Scan(&raw, &key); e != nil {
				return e
			}
			if e = json.Unmarshal([]byte(raw), &p.Photo); e != nil {
				return e
			}
			p.GroupKey = key
			p.GroupLabel = labelMap[key]
			itemsByGroup[key] = append(itemsByGroup[key], p)
		}
		return rows.Err()
	}
	labels := map[string]string{}
	for _, g := range selected {
		labels[g.key] = g.label
	}
	if len(normalKeys) > 0 {
		placeholders := "?" + strings.Repeat(",?", len(normalKeys)-1)
		normalCTE := cte
		pageArgs := append([]any{}, memberArgs...)
		if kind == "people" {
			normalCTE = strings.Replace(cte, "WHERE ai.models=?", "WHERE f.person_id IN ("+placeholders+") AND ai.models=?", 1)
			pageArgs = append([]any{}, args...)
			for _, key := range normalKeys {
				pageArgs = append(pageArgs, key)
			}
			pageArgs = append(pageArgs, a.models)
		}
		for _, key := range normalKeys {
			pageArgs = append(pageArgs, key)
		}
		pageArgs = append(pageArgs, sortArgs...)
		pageArgs = append(pageArgs, normalTake, normalSkip)
		pageArgs = append(pageArgs, sortArgs...)
		groupDirection := "ASC"
		if descending {
			groupDirection = "DESC"
		}
		orderBy := `m.group_label COLLATE NOCASE ` + groupDirection + `,m.group_key,` + order
		query := normalCTE + `, page AS MATERIALIZED(SELECT i.id,m.group_key,m.group_label FROM labels m JOIN catalog.images i ON i.id=m.image_id WHERE m.group_key IN (` + placeholders + `) ORDER BY ` + orderBy + ` LIMIT ? OFFSET ?) SELECT metadata_json(i.metadata),m.group_key FROM page m JOIN catalog.images i ON i.id=m.id ORDER BY ` + orderBy
		if err = readPage(query, pageArgs, labels); err != nil {
			return err
		}
	}
	for _, g := range selected {
		if g.key == fallbackKey {
			pageArgs := append(append([]any{}, memberArgs...), args...)
			pageArgs = append(pageArgs, sortArgs...)
			pageArgs = append(pageArgs, g.take, g.skip)
			pageArgs = append(pageArgs, sortArgs...)
			query := cte + `, page AS MATERIALIZED(SELECT i.id FROM catalog.images i WHERE ` + clause + ` AND i.id NOT IN(SELECT image_id FROM labels) ORDER BY ` + order + ` LIMIT ? OFFSET ?) SELECT metadata_json(i.metadata),'` + fallbackKey + `' FROM page m JOIN catalog.images i ON i.id=m.id ORDER BY ` + order
			if err = readPage(query, pageArgs, labels); err != nil {
				return err
			}
		}
	}
	items := []groupedPhoto{}
	for _, g := range selected {
		items = append(items, itemsByGroup[g.key]...)
	}
	return writeJSON(w, 200, map[string]any{"items": items, "total": total, "unique_total": unique, "offset": offset, "semantic": false, "group_by": kind})
}

func (a *AI) simpleGroupedSearch(w http.ResponseWriter, r *http.Request, clause string, args []any, order string, sortArgs []any, limit, offset int, kind string) error {
	key, label := "''", "'No tags'"
	switch kind {
	case "path":
		key = "i.directory"
		label = "coalesce(nullif(i.directory,''),'Root folder')"
	case "month":
		key = "CASE WHEN replace(substr(coalesce(nullif(i.taken_at,''),i.created_at),1,7),':','-') GLOB '[0-9][0-9][0-9][0-9]-[0-1][0-9]' AND substr(coalesce(nullif(i.taken_at,''),i.created_at),6,2) BETWEEN '01' AND '12' THEN replace(substr(coalesce(nullif(i.taken_at,''),i.created_at),1,7),':','-') ELSE 'Unknown month' END"
		label = key
	case "year":
		key = "coalesce(nullif(substr(coalesce(nullif(i.taken_at,''),i.created_at),1,4),''),'Unknown year')"
		label = key
	}
	var total int
	if err := a.read.QueryRowContext(r.Context(), "SELECT count(*) FROM catalog.images i WHERE "+clause, args...).Scan(&total); err != nil {
		return err
	}
	direction := "ASC"
	if r.URL.Query().Get("direction") == "desc" {
		direction = "DESC"
	}
	groupOrder := key + " COLLATE NOCASE " + direction + "," + order
	if kind == "year" || kind == "month" {
		// Group boundaries precede the photo sort, so their direction must
		// agree too. Missing dates stay at the end in either direction.
		fallback := "Unknown year"
		if kind == "month" {
			fallback = "Unknown month"
		}
		groupOrder = "CASE WHEN " + key + "='" + fallback + "' THEN 1 ELSE 0 END," + key + " COLLATE NOCASE " + direction + "," + order
	}
	if kind == "empty_tag" {
		groupOrder = order
	}
	pageArgs := append(append([]any{}, args...), sortArgs...)
	pageArgs = append(pageArgs, limit, offset)
	pageArgs = append(pageArgs, sortArgs...)
	rows, err := a.read.QueryContext(r.Context(), "WITH page AS MATERIALIZED(SELECT i.id,"+key+" group_key,"+label+" group_label FROM catalog.images i WHERE "+clause+" ORDER BY "+groupOrder+" LIMIT ? OFFSET ?) SELECT metadata_json(i.metadata),m.group_key,m.group_label FROM page m JOIN catalog.images i ON i.id=m.id ORDER BY "+groupOrder, pageArgs...)
	if err != nil {
		return err
	}
	defer rows.Close()
	items := []groupedPhoto{}
	for rows.Next() {
		var raw string
		var p groupedPhoto
		if err = rows.Scan(&raw, &p.GroupKey, &p.GroupLabel); err != nil {
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
	return writeJSON(w, 200, map[string]any{"items": items, "total": total, "unique_total": total, "offset": offset, "semantic": false, "group_by": r.URL.Query().Get("group_by")})
}

// Match SQLite NOCASE (ASCII folding) so page offsets agree with SQL ordering.
func foldGroupLabel(value string) string {
	return strings.Map(func(r rune) rune {
		if r >= 'A' && r <= 'Z' {
			return r + ('a' - 'A')
		}
		return r
	}, value)
}
