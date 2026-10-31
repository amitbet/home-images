package main

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"
	"time"
)

type playbackResult struct {
	Photo
	Similarity float32 `json:"similarity"`
	Playback   struct {
		Mode, Order string
		PoolSize    int    `json:"pool_size"`
		CachedAt    string `json:"cached_at"`
	} `json:"playback"`
}

func TestPlaybackAfterSQLiteConnectionReplacement(t *testing.T) {
	l, s, _ := fixture(t)
	first := getPlayback(t, s, "mode=metadata&order=sequential")
	for _, db := range []*sql.DB{l.db, l.ai.db, l.ai.read} {
		conn, err := db.Conn(context.Background())
		if err != nil {
			t.Fatal(err)
		}
		_ = conn.Raw(func(any) error { return driver.ErrBadConn })
		_ = conn.Close()
	}
	next := getPlayback(t, s, "mode=metadata&order=sequential&after="+first.ID)
	if next.ID == first.ID {
		t.Fatal("cursor failed after reconnection")
	}
	for _, path := range []string{"/api/albums", "/api/collections", "/api/search?mode=metadata", "/api/next?mode=sequential", "/api/indexing"} {
		if getJSON(t, s, path, nil) != 200 {
			t.Fatal("attachment lost after reconnect", path)
		}
	}
	if _, err := l.ai.read.Exec("INSERT INTO ai_settings VALUES('must_remain_readonly','true')"); err == nil {
		t.Fatal("replacement reader lost query_only")
	}
}

func TestOtherReadsContinueWhileSearchConnectionIsBusy(t *testing.T) {
	l, s, _ := fixture(t)
	conn, err := l.ai.read.Conn(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	client := &http.Client{Timeout: time.Second}
	for _, path := range []string{"/api/albums", "/api/collections", "/api/playback/next?mode=metadata&order=sequential"} {
		resp, err := client.Get(s.URL + path)
		if err != nil {
			t.Fatal("busy search blocked independent reads", path, err)
		}
		resp.Body.Close()
		if resp.StatusCode != 200 {
			t.Fatal(path, resp.Status)
		}
	}
}

func TestPlaybackRefreshesExpiredSnapshotInBackground(t *testing.T) {
	l, s, _ := fixture(t)
	first := getPlayback(t, s, "mode=metadata&order=sequential")
	if postTest(t, s, "/api/curation", `{"ids":["`+first.ID+`"]}`) != 200 {
		t.Fatal("annotation read")
	}
	still := getPlayback(t, s, "mode=metadata&order=sequential")
	if still.Playback.CachedAt != first.Playback.CachedAt {
		t.Fatal("annotation reads invalidated playback")
	}
	expired := time.Now().Add(-3 * time.Minute)
	l.ai.playMu.Lock()
	for key, item := range l.ai.playCache {
		item.Created = expired
		l.ai.playCache[key] = item
	}
	l.ai.playMu.Unlock()
	old := getPlayback(t, s, "mode=metadata&order=sequential")
	if old.Playback.CachedAt != expired.UTC().Format(time.RFC3339Nano) {
		t.Fatal("expired snapshot was rebuilt synchronously")
	}
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		fresh := false
		l.ai.playMu.Lock()
		for _, item := range l.ai.playCache {
			fresh = fresh || item.Created.After(expired)
		}
		l.ai.playMu.Unlock()
		if fresh {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("background refresh did not complete")
}

func getPlayback(t *testing.T, s *httptest.Server, query string) playbackResult {
	t.Helper()
	var result playbackResult
	if code := getJSON(t, s, "/api/playback/next?"+query, &result); code != 200 {
		t.Fatalf("playback HTTP %d: %+v", code, result)
	}
	return result
}

func TestPlaybackAlbumsFiltersAndCollections(t *testing.T) {
	l, s, _ := fixture(t)
	ctx := context.Background()
	a := l.ai
	album, err := a.db.Exec("INSERT INTO curation.albums(root,name,created_at) VALUES(?,?,?)", l.rootPath, "Summer", time.Now().UTC().Format(time.RFC3339))
	if err != nil {
		t.Fatal(err)
	}
	albumID, _ := album.LastInsertId()
	for _, path := range []string{"a.jpg", "b.jpg"} {
		if _, err = a.db.Exec("INSERT INTO curation.album_images VALUES(?,?)", albumID, imageID(path)); err != nil {
			t.Fatal(err)
		}
	}
	query := "mode=metadata&order=sequential&album=" + fmtID(albumID)
	first := getPlayback(t, s, query)
	if first.Path != "a.jpg" {
		t.Fatal(first.Path)
	}
	second := getPlayback(t, s, query+"&after="+first.ID)
	if second.Path != "b.jpg" {
		t.Fatal(second.Path)
	}
	if first.Playback.CachedAt == "" || second.Playback.CachedAt != first.Playback.CachedAt {
		t.Fatal("metadata selection was rescanned instead of cached")
	}
	wrap := getPlayback(t, s, query+"&after="+second.ID)
	if wrap.ID != first.ID {
		t.Fatal("did not wrap")
	}
	random := getPlayback(t, s, "mode=metadata&order=random&album="+fmtID(albumID)+"&after="+first.ID)
	if random.ID != second.ID {
		t.Fatal("immediate repeat")
	}
	if random.Playback.CachedAt != first.Playback.CachedAt {
		t.Fatal("playback orders must share metadata cache")
	}
	if postTest(t, s, "/api/curation/"+first.ID, `{"favorite":true,"title":"Our Summer_100%","location":"Paris","tags":["holiday"]}`) != 200 {
		t.Fatal("annotation")
	}
	favorite := getPlayback(t, s, "mode=metadata&favorite=true&place=Paris&tag=holiday&from=2020-01-01&to=2020-12-31&order=sequential")
	if favorite.ID != first.ID {
		t.Fatal("filters ignored")
	}
	var search struct {
		Items []Photo
		Total int
	}
	getJSON(t, s, "/api/search?mode=metadata&q="+url.QueryEscape("summer_100%"), &search)
	if search.Total != 1 || search.Items[0].ID != first.ID {
		t.Fatal("annotation search/literal wildcards", search)
	}
	if getJSON(t, s, "/api/playback/next?mode=metadata&q="+url.QueryEscape("Summer_100%"), &favorite) != 200 || favorite.ID != first.ID {
		t.Fatal("playback annotation search")
	}
	if getJSON(t, s, "/api/playback/next?mode=metadata&place=Nowhere", nil) != 404 {
		t.Fatal("empty selection")
	}
	saved, _ := json.Marshal(map[string]string{"mode": "metadata", "album": fmtID(albumID), "favorite": "true"})
	created, err := a.db.Exec("INSERT INTO curation.collections(root,name,query,created_at) VALUES(?,?,?,?)", l.rootPath, "Favorites", string(saved), "now")
	if err != nil {
		t.Fatal(err)
	}
	collection, _ := created.LastInsertId()
	selected := getPlayback(t, s, "collection="+fmtID(collection)+"&order=sequential")
	if selected.ID != first.ID {
		t.Fatal("collection did not load")
	}
	selected = getPlayback(t, s, "collection="+fmtID(collection)+"&favorite=&after="+first.ID+"&order=sequential")
	if selected.ID != second.ID {
		t.Fatal("empty override did not remove saved filter")
	}
	wrong, err := a.db.Exec("INSERT INTO curation.collections(root,name,query,created_at) VALUES(?,?,?,?)", "different root", "Foreign", string(saved), "now")
	if err != nil {
		t.Fatal(err)
	}
	foreign, _ := wrong.LastInsertId()
	if getJSON(t, s, "/api/playback/next?collection="+fmtID(foreign), nil) != 404 {
		t.Fatal("collection root confinement failed")
	}
	// Metadata playback does not need the ML worker or an AI index.
	jobs, err := a.jobs(ctx, "")
	if err != nil || len(jobs) == 0 {
		t.Fatal("unexpected AI state", err)
	}
}

func TestSemanticPlaybackRankCacheAndVisibility(t *testing.T) {
	l, s, _ := fixture(t)
	a := l.ai
	ctx := context.Background()
	a.lib.config.ML.URL = "http://unused"
	a.textCache["beach"] = vector{1, 0, 0}
	for i, path := range []string{"a.jpg", "b.jpg", "album_100%/c.jpg"} {
		prediction := syntheticPrediction()
		prediction.Clip = []json.RawMessage{json.RawMessage(`"[1,0,0]"`), json.RawMessage(`"[0.8,0.6,0]"`), json.RawMessage(`"[-1,0,0]"`)}[i]
		if err := a.save(ctx, testJob(t, l, path), prediction); err != nil {
			t.Fatal(err)
		}
	}
	query := "mode=semantic&q=beach&order=sequential&pool_limit=2"
	first := getPlayback(t, s, query)
	if first.Path != "a.jpg" || first.Playback.PoolSize != 2 {
		t.Fatal(first)
	}
	second := getPlayback(t, s, query+"&after="+first.ID)
	if second.Path != "b.jpg" || second.Playback.CachedAt != first.Playback.CachedAt {
		t.Fatal("ranking or cache reuse failed", second)
	}
	wrap := getPlayback(t, s, query+"&after="+second.ID)
	if wrap.ID != first.ID {
		t.Fatal("semantic wrap")
	}
	random := getPlayback(t, s, "mode=semantic&q=beach&order=random&pool_limit=2&after="+first.ID)
	if random.ID != second.ID || random.Playback.CachedAt != first.Playback.CachedAt {
		t.Fatal("random should share selection cache and avoid immediate repeat", random)
	}
	// A deleted catalog row must never be returned from an otherwise valid cache.
	if _, err := l.db.Exec("DELETE FROM images WHERE id=?", first.ID); err != nil {
		t.Fatal(err)
	}
	current := getPlayback(t, s, query)
	if current.ID != second.ID || current.Playback.CachedAt != first.Playback.CachedAt {
		t.Fatal("stale cached ID not revalidated", current)
	}
	// Curation changes invalidate the snapshot immediately, not at the TTL boundary.
	if postTest(t, s, "/api/curation/"+second.ID, `{"hidden":true}`) != 200 {
		t.Fatal("hide")
	}
	current = getPlayback(t, s, query)
	if current.Path != "album_100%/c.jpg" || current.Playback.CachedAt == first.Playback.CachedAt {
		t.Fatal("hidden content leaked from cache", current)
	}
	if getJSON(t, s, "/api/playback/next?mode=semantic&q=beach&pool_limit=2001", nil) != 422 {
		t.Fatal("unbounded pool accepted")
	}
	if getJSON(t, s, "/api/playback/next?order=invalid", nil) != 400 {
		t.Fatal("invalid order accepted")
	}
}

func TestHiddenFaceEndpoints(t *testing.T) {
	l, s, _ := fixture(t)
	a := l.ai
	ctx := context.Background()
	first := testJob(t, l, "a.jpg")
	second := testJob(t, l, "b.jpg")
	if err := a.save(ctx, first, syntheticPrediction()); err != nil {
		t.Fatal(err)
	}
	pred := syntheticPrediction()
	pred.Faces[0].Embedding = json.RawMessage(`"[0,1,0]"`)
	if err := a.save(ctx, second, pred); err != nil {
		t.Fatal(err)
	}
	var faces []map[string]any
	getJSON(t, s, "/api/images/"+second.ID+"/faces", &faces)
	if len(faces) != 1 {
		t.Fatal(faces)
	}
	person := int64(faces[0]["person_id"].(float64))
	if postTest(t, s, "/api/curation/"+second.ID, `{"hidden":true}`) != 200 {
		t.Fatal("hide")
	}
	getJSON(t, s, "/api/images/"+second.ID+"/faces", &faces)
	if len(faces) != 0 {
		t.Fatal("hidden faces exposed")
	}
	getJSON(t, s, "/api/images/"+second.ID+"/faces?hidden=all", &faces)
	if len(faces) != 1 {
		t.Fatal("explicit hidden faces missing")
	}
	var groups struct {
		Items []personSummary
		Total int
	}
	getJSON(t, s, "/api/people", &groups)
	if groups.Total != 1 || groups.Items[0].Count != 1 {
		t.Fatal("hidden faces counted", groups)
	}
	getJSON(t, s, "/api/people?hidden=all", &groups)
	if groups.Total != 2 {
		t.Fatal("hidden=all ignored", groups)
	}
	resp, err := s.Client().Get(s.URL + "/api/people/" + fmtID(person) + "/image")
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != 404 {
		t.Fatal("hidden thumbnail exposed", resp.Status)
	}
	resp, err = s.Client().Get(s.URL + "/api/people/" + fmtID(person) + "/image?hidden=all")
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != 200 {
		t.Fatal("explicit hidden thumbnail unavailable", resp.Status)
	}
}

func TestPlaybackPeopleAnyAndNone(t *testing.T) {
	l, s, _ := fixture(t)
	ctx := context.Background()
	if err := l.ai.save(ctx, testJob(t, l, "a.jpg"), syntheticPrediction()); err != nil {
		t.Fatal(err)
	}
	empty := syntheticPrediction()
	empty.Faces = nil
	if err := l.ai.save(ctx, testJob(t, l, "b.jpg"), empty); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 10; i++ {
		if p := getPlayback(t, s, "order=random&group_kind=people&group_key=any"); p.Path != "a.jpg" {
			t.Fatal("photo without faces returned for any", p.Path)
		}
		if p := getPlayback(t, s, "order=random&group_kind=people&group_key=none"); p.Path == "a.jpg" {
			t.Fatal("photo with faces returned for none")
		}
	}
}
