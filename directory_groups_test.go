package main

import (
	"net/url"
	"testing"
)

func TestDirectoryGroupsAndExactScope(t *testing.T) {
	l, s, _ := fixture(t)
	a := testJob(t, l, "a.jpg")
	b := testJob(t, l, "b.jpg")
	c := testJob(t, l, "album_100%/c.jpg")
	if _, err := l.db.Exec("DELETE FROM images WHERE id NOT IN(?,?,?)", a.ID, b.ID, c.ID); err != nil {
		t.Fatal(err)
	}
	l.db.Exec("UPDATE images SET taken_at='2022-01-01' WHERE id=?", a.ID)
	l.db.Exec("UPDATE images SET taken_at='2020-01-01' WHERE id=?", b.ID)
	l.db.Exec("UPDATE images SET taken_at='2023-01-01' WHERE id=?", c.ID)
	var result struct {
		Items []struct {
			Key   string
			Count int
			Cover string
		}
		Total int
	}
	if getJSON(t, s, "/api/search?group_by=directory", &result) != 200 || result.Total != 2 || len(result.Items) != 2 {
		t.Fatal(result)
	}
	if result.Items[0].Key != "" || result.Items[0].Count != 2 {
		t.Fatal(result)
	}
	var photos struct {
		Items []Photo
		Total int
	}
	if getJSON(t, s, "/api/search?group_kind=directory&group_key=", &photos) != 200 || photos.Total != 2 || photos.Items[0].ID != b.ID {
		t.Fatal("exact root or default time sort", photos)
	}
	if getJSON(t, s, "/api/search?group_kind=directory&group_key="+url.QueryEscape("album_100%"), &photos) != 200 || photos.Total != 1 || photos.Items[0].ID != c.ID {
		t.Fatal("exact folder", photos)
	}
	if getJSON(t, s, "/api/search?group_kind=year&group_key=2020", &photos) != 200 || photos.Total != 1 || photos.Items[0].ID != b.ID {
		t.Fatal("year scope", photos)
	}
	postTest(t, s, "/api/curation/"+c.ID, `{"hidden":true}`)
	if getJSON(t, s, "/api/search?group_by=directory", &result) != 200 || result.Total != 1 {
		t.Fatal("hidden folder", result)
	}
}
