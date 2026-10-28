package main

import (
	"context"
	"fmt"
	"log"
	"net/http"
	"time"
)

const peopleSummaryInterval = 30 * time.Second

type peopleSnapshot struct {
	items    []personSummary
	revision uint64
	created  time.Time
}

func defaultPeopleFilters(r *http.Request) bool {
	for _, key := range []string{"favorite", "album", "from", "to", "on_this_day", "place", "tag"} {
		if r.URL.Query().Get(key) != "" {
			return false
		}
	}
	hidden := r.URL.Query().Get("hidden")
	return hidden == "" || hidden == "false"
}

// The overview is refreshed off the request path while background indexing runs.
// API edits invalidate it immediately; callers share one cancellable rebuild.
func (a *AI) peopleSummaries(ctx context.Context, refresh bool) (*peopleSnapshot, error) {
	for {
		revision := a.selectionRevision.Load()
		a.peopleMu.Lock()
		if cached := a.peopleSummary; !refresh && cached != nil && cached.revision == revision && time.Since(cached.created) < 2*peopleSummaryInterval {
			a.peopleMu.Unlock()
			return cached, nil
		}
		if building := a.peopleBuilding; building != nil {
			a.peopleMu.Unlock()
			select {
			case <-ctx.Done():
				return nil, ctx.Err()
			case <-building:
				refresh = false
				continue
			}
		}
		building := make(chan struct{})
		a.peopleBuilding = building
		a.peopleMu.Unlock()

		items, err := a.queryPeopleSummaries(ctx)
		next := &peopleSnapshot{items: items, revision: revision, created: time.Now()}
		a.peopleMu.Lock()
		if err == nil {
			a.peopleSummary = next
		}
		a.peopleBuilding = nil
		close(building)
		a.peopleMu.Unlock()
		if err != nil {
			return nil, err
		}
		// A manual edit during the read must not publish its old names/counts.
		if revision != a.selectionRevision.Load() {
			refresh = false
			continue
		}
		return next, nil
	}
}

func (a *AI) queryPeopleSummaries(ctx context.Context) ([]personSummary, error) {
	rows, err := a.read.QueryContext(ctx, `SELECT p.id,p.name,count(DISTINCT f.image_id)`+personYearRangeSQL+`
 FROM people p JOIN faces f ON f.person_id=p.id
 JOIN ai_images a ON a.image_id=f.image_id
 JOIN (SELECT id,signature,taken_at FROM catalog.images INDEXED BY image_person_capture
 WHERE id NOT IN(SELECT image_id FROM curation.annotations WHERE root=? AND hidden=1)) i
 ON i.id=a.image_id AND i.signature=a.signature
 WHERE a.models=? AND a.error='' AND p.merged_into IS NULL
 GROUP BY p.id ORDER BY count(DISTINCT f.image_id) DESC,p.id`, a.lib.rootPath, a.models)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	items := []personSummary{}
	for rows.Next() {
		var p personSummary
		if err := rows.Scan(&p.ID, &p.Name, &p.Count, &p.FirstYear, &p.LastYear); err != nil {
			return nil, err
		}
		p.ImageURL = fmt.Sprintf("/api/people/%d/image", p.ID)
		items = append(items, p)
	}
	return items, rows.Err()
}

func (a *AI) cachedPeoplePage(w http.ResponseWriter, r *http.Request, limit, offset int) error {
	snapshot, err := a.peopleSummaries(r.Context(), r.URL.Query().Get("refresh") == "1")
	if err != nil {
		return err
	}
	total := len(snapshot.items)
	return writeJSON(w, 200, map[string]any{"items": snapshot.items[min(offset, total):min(offset+limit, total)], "total": total, "offset": offset, "summary_age_ms": time.Since(snapshot.created).Milliseconds()})
}

func (a *AI) warmPeopleSummaries(ctx context.Context) {
	ticker := time.NewTicker(peopleSummaryInterval)
	defer ticker.Stop()
	for {
		if _, err := a.peopleSummaries(ctx, true); err != nil && ctx.Err() == nil {
			log.Printf("Refresh people summaries: %v", err)
		}
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}
