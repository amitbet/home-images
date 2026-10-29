package main

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"testing"
)

func TestPhotoGroupFoldersAndDrilldown(t *testing.T) {
	l, s, _ := fixture(t)
	l.ai.textCache["group test"] = vector{1, 0, 0}
	l.config.ML.URL = "http://unused"
	for n, path := range []string{"a.jpg", "b.jpg", "album_100%/c.jpg"} {
		job := testJob(t, l, path)
		prediction := syntheticPrediction()
		if n == 2 {
			prediction.Faces = nil
		}
		if err := l.ai.save(context.Background(), job, prediction); err != nil {
			t.Fatal(err)
		}
		l.db.Exec("UPDATE images SET taken_at=? WHERE id=?", fmt.Sprintf("202%d-01-01", n), job.ID)
	}
	l.db.Exec("DELETE FROM images WHERE path NOT IN('a.jpg','b.jpg','album_100%/c.jpg')")
	postTest(t, s, "/api/curation/"+imageID("a.jpg"), `{"tags":["Family","Travel"]}`)
	postTest(t, s, "/api/curation/"+imageID("b.jpg"), `{"tags":["family"]}`)
	for _, mode := range []string{"", "&q=group+test"} {
		for _, kind := range []string{"path", "year", "people", "tag"} {
			base := "/api/search?group_by=" + kind + mode
			var flat struct {
				Items []groupedPhoto
				Total int
			}
			if getJSON(t, s, base+"&limit=200", &flat) != 200 {
				t.Fatal(kind)
			}
			members := map[string]map[string]bool{}
			for _, p := range flat.Items {
				if members[p.GroupKey] == nil {
					members[p.GroupKey] = map[string]bool{}
				}
				members[p.GroupKey][p.ID] = true
			}
			type card struct {
				Key, Label, Cover string
				Count             int
			}
			var folders struct {
				Items   []card
				Total   int
				Folders bool `json:"photo_groups"`
			}
			if getJSON(t, s, base+"&group_view=folders", &folders) != 200 || !folders.Folders || folders.Total != len(members) {
				t.Fatalf("%s %s %+v", kind, mode, folders)
			}
			for offset, g := range folders.Items {
				if g.Count != len(members[g.Key]) || !members[g.Key][g.Cover] || g.Label == "" {
					t.Fatal("invalid group card", g, members)
				}
				var page struct {
					Items []card
					Total int
				}
				getJSON(t, s, base+fmt.Sprintf("&group_view=folders&limit=1&offset=%d", offset), &page)
				if page.Total != folders.Total || len(page.Items) != 1 || page.Items[0].Key != g.Key {
					t.Fatal("group pagination", page)
				}
				var photos struct {
					Items []Photo
					Total int
				}
				query := "/api/search?group_kind=" + kind + "&group_key=" + url.QueryEscape(g.Key) + mode
				if getJSON(t, s, query, &photos) != 200 || photos.Total != g.Count {
					t.Fatal("group drilldown", query, photos.Total, g.Count)
				}
				for _, p := range photos.Items {
					if !members[g.Key][p.ID] {
						t.Fatal("photo outside selected group")
					}
				}
			}
		}
	}
	var response map[string]json.RawMessage
	if getJSON(t, s, "/api/search?group_by=invalid&group_view=folders", &response) != 400 {
		t.Fatal("invalid group accepted")
	}
}
