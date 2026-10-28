package main

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"net/url"
	"testing"
)

type semanticGroupResponse struct {
	Items []struct {
		groupedPhoto
		Similarity float32 `json:"similarity"`
	}
	Total, Offset int
	Unique        int `json:"unique_total"`
	Candidates    int `json:"indexed_candidates"`
	RankedLimit   int `json:"ranked_limit"`
	Semantic      bool
}

func TestSemanticGroupingRankingMembershipsAndFilters(t *testing.T) {
	l, s, _ := fixture(t)
	a := l.ai
	a.lib.config.ML.URL = "http://unused"
	a.textCache["beach"] = vector{1, 0, 0}
	paths := []string{"a.jpg", "b.jpg", "album_100%/c.jpg", "albumX100other/d.jpg"}
	for n, path := range paths {
		p := syntheticPrediction()
		p.Clip = json.RawMessage(fmt.Sprintf(`"[%f,%f,0]"`, 1-float64(n)*0.2, float64(n)*0.2))
		if n == 2 {
			p.Faces = nil
		}
		if err := a.save(context.Background(), testJob(t, l, path), p); err != nil {
			t.Fatal(err)
		}
	}
	// Two tags and two identical face memberships must not duplicate one entry
	// within a group. Missing labels still have their own final group.
	if postTest(t, s, "/api/curation/"+imageID(paths[0]), `{"tags":["Alpha","Zulu"],"favorite":true}`) != 200 {
		t.Fatal("tags")
	}
	if postTest(t, s, "/api/curation/"+imageID(paths[1]), `{"tags":["alpha"]}`) != 200 {
		t.Fatal("tags")
	}
	if postTest(t, s, "/api/curation/"+imageID(paths[3]), `{"hidden":true}`) != 200 {
		t.Fatal("hide")
	}
	if _, err := a.db.Exec("INSERT INTO faces(image_id,person_id,box,score,width,height) SELECT image_id,person_id,box,score,width,height FROM faces WHERE image_id=?", imageID(paths[0])); err != nil {
		t.Fatal(err)
	}
	if _, err := l.db.Exec("UPDATE images SET taken_at='2021-01-01',created_at=NULL WHERE id=?", imageID(paths[1])); err != nil {
		t.Fatal(err)
	}
	if _, err := l.db.Exec("UPDATE images SET taken_at=NULL,created_at=NULL WHERE id=?", imageID(paths[2])); err != nil {
		t.Fatal(err)
	}
	var person int64
	if err := a.db.QueryRow("SELECT person_id FROM faces WHERE image_id=?", imageID(paths[0])).Scan(&person); err != nil {
		t.Fatal(err)
	}
	for _, filter := range []string{"", "&favorite=true", "&person=" + fmtID(person), "&directory=album_100%25&recursive=false"} {
		var flat struct {
			Items []struct {
				Photo
				Similarity float32 `json:"similarity"`
			}
		}
		if code := getJSON(t, s, "/api/search?q=beach&limit=200"+filter, &flat); code != 200 {
			t.Fatal(code)
		}
		ranks := map[string]int{}
		scores := map[string]float32{}
		for n, p := range flat.Items {
			ranks[p.ID] = n
			scores[p.ID] = p.Similarity
		}
		for _, kind := range []string{"path", "year", "people", "tag"} {
			for _, direction := range []string{"asc", "desc"} {
				query := "/api/search?q=beach&group_by=" + kind + "&direction=" + direction + filter
				var all semanticGroupResponse
				if code := getJSON(t, s, query, &all); code != 200 || !all.Semantic || all.Unique != len(flat.Items) || all.Total != len(all.Items) {
					t.Fatalf("%s: code=%d %+v", query, code, all)
				}
				seen := map[string]bool{}
				previousKey, previousLabel, previousRank := "", "", -1
				for n, p := range all.Items {
					rank, exists := ranks[p.ID]
					if !exists || p.Similarity != scores[p.ID] {
						t.Fatal("group admitted an unranked photo", query, p)
					}
					membership := p.ID + ":" + p.GroupKey
					if seen[membership] {
						t.Fatal("duplicate membership", query, p)
					}
					seen[membership] = true
					if n > 0 && p.GroupKey == previousKey && rank <= previousRank {
						t.Fatal("similarity order lost", query, all)
					}
					label := foldGroupLabel(p.GroupLabel)
					if kind == "path" || kind == "year" {
						label = foldGroupLabel(p.GroupKey)
					}
					fallback := (kind == "tag" && p.GroupKey == "") || (kind == "people" && p.GroupKey == "none") || (kind == "year" && p.GroupKey == "Unknown year")
					if n > 0 && p.GroupKey != previousKey && !fallback && ((direction == "asc" && label < previousLabel) || (direction == "desc" && label > previousLabel)) {
						t.Fatal("group order", query, all)
					}
					previousKey, previousLabel, previousRank = p.GroupKey, label, rank
					var page semanticGroupResponse
					if code := getJSON(t, s, fmt.Sprintf("%s&limit=1&offset=%d", query, n), &page); code != 200 || page.Total != all.Total || len(page.Items) != 1 || page.Items[0].ID != p.ID || page.Items[0].GroupKey != p.GroupKey || page.Items[0].Similarity != p.Similarity {
						t.Fatal("unstable pagination", query, n, page)
					}
				}
			}
		}
	}
	// Cached ranked IDs must not keep a photo eligible after its source changes.
	if _, err := l.db.Exec("UPDATE images SET signature='changed' WHERE id=?", imageID(paths[0])); err != nil {
		t.Fatal(err)
	}
	var stale semanticGroupResponse
	if code := getJSON(t, s, "/api/search?q=beach&group_by=tag&direction=asc", &stale); code != 200 || stale.Unique != 2 {
		t.Fatal("stale cached match", code, stale)
	}
	for _, p := range stale.Items {
		if p.ID == imageID(paths[0]) {
			t.Fatal("stale photo retained")
		}
	}
}

func TestSemanticGroupingBoundsPoolBeforeMembershipsAndPages(t *testing.T) {
	l, s, _ := fixture(t)
	a := l.ai
	a.lib.config.ML.URL = "http://unused"
	a.textCache["pool"] = vector{1, 0, 0}
	catalog, err := l.db.Begin()
	if err != nil {
		t.Fatal(err)
	}
	defer catalog.Rollback()
	ai, err := a.db.Begin()
	if err != nil {
		t.Fatal(err)
	}
	defer ai.Rollback()
	for n := 0; n <= semanticGroupPool; n++ {
		id := fmt.Sprintf("pool-%04d", n)
		raw, _ := json.Marshal(Photo{ID: id, Path: id + ".jpg", Directory: "pool"})
		if _, err = catalog.Exec("INSERT INTO images(id,path,directory,signature,metadata,search) VALUES(?,?,'pool','sig',metadata_pack(?),'')", id, id+".jpg", string(raw)); err != nil {
			t.Fatal(err)
		}
		x := 1 - float32(n)/float32(semanticGroupPool*2)
		v := vector{x, float32(math.Sqrt(1 - float64(x)*float64(x))), 0}
		if _, err = ai.Exec("INSERT INTO ai_images(image_id,signature,models,clip) VALUES(?,'sig',?,?)", id, a.models, v.blob()); err != nil {
			t.Fatal(err)
		}
		if _, err = ai.Exec("INSERT INTO curation.annotations(root,image_id,tags,updated_at) VALUES(?,?,?,?)", l.rootPath, id, `["Alpha","Zulu"]`, "now"); err != nil {
			t.Fatal(err)
		}
	}
	if err = catalog.Commit(); err != nil {
		t.Fatal(err)
	}
	if err = ai.Commit(); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		offset  int
		id, key string
	}{{1001, "pool-1001", "alpha"}, {1199, "pool-1199", "alpha"}, {1200, "pool-0000", "zulu"}, {2399, "pool-1199", "zulu"}} {
		var page semanticGroupResponse
		path := fmt.Sprintf("/api/search?q=pool&group_by=tag&limit=1&offset=%d", tc.offset)
		if code := getJSON(t, s, path, &page); code != 200 || page.Unique != semanticGroupPool || page.Total != semanticGroupPool*2 || page.Candidates != semanticGroupPool+1 || len(page.Items) != 1 || page.Items[0].ID != tc.id || page.Items[0].GroupKey != tc.key {
			t.Fatal(path, code, page)
		}
	}
	var empty semanticGroupResponse
	if code := getJSON(t, s, "/api/search?q=pool&group_by=tag&offset=2400", &empty); code != 200 || len(empty.Items) != 0 || empty.Total != 2400 {
		t.Fatal(code, empty)
	}
	// An empty match set stays a valid grouped response with explicit zero counts.
	var missing semanticGroupResponse
	if code := getJSON(t, s, "/api/search?q=pool&group_by=people&directory="+url.QueryEscape("empty"), &missing); code != 200 || missing.Total != 0 || missing.Unique != 0 || missing.Items == nil {
		t.Fatal(code, missing)
	}
}
