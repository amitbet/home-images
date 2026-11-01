package main

import (
	"bytes"
	"context"
	"fmt"
	"math"
	"math/rand"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"testing"
	"time"
)

func TestPersonHNSWPersistenceAndRevision(t *testing.T) {
	l, _, _ := fixture(t)
	a := l.ai
	ctx := context.Background()
	insert := func(v vector, group any) int64 {
		t.Helper()
		r, err := a.db.Exec("INSERT INTO people(model,embedding,merged_into) VALUES(?,?,?)", l.config.ML.FaceModel, v.blob(), group)
		if err != nil {
			t.Fatal(err)
		}
		id, _ := r.LastInsertId()
		return id
	}
	id := insert(vector{1, 0, 0}, nil)
	other := insert(vector{0, 1, 0}, nil)
	alias := insert(vector{0, 0, 1}, id)
	insert(vector{float32(math.NaN()), 0, 0}, nil)
	insert(vector{0, 0, 0}, nil)
	index, err := a.loadPersonHNSW(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(index.groups) != 3 || len(index.vectors[id]) != 2 {
		t.Fatal("lost aliases or retained invalid vectors")
	}
	path := filepath.Join(l.config.DataDir, "people.hnsw")
	saved, err := readPersonHNSW(path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = restorePersonHNSW(saved); err != nil {
		t.Fatal("reload", err)
	}
	info, _ := os.Stat(path)
	a.personIndex = nil
	loaded, err := a.loadPersonHNSW(ctx)
	if err != nil {
		t.Fatal(err)
	}
	after, _ := os.Stat(path)
	if !info.ModTime().Equal(after.ModTime()) || len(loaded.vectors[id]) != 2 {
		t.Fatal("valid cache rebuilt")
	}
	// Prebuilt indexes are adopted only after a full vector comparison, with
	// database identity and group mappings rebound to this installation.
	saved.Root, saved.Revision = "offline build", "old revision"
	if err = writePersonHNSW(path, saved); err != nil {
		t.Fatal(err)
	}
	a.personIndex = nil
	loaded, err = a.loadPersonHNSW(ctx)
	if err != nil {
		t.Fatal(err)
	}
	rebound, err := readPersonHNSW(path)
	if err != nil || rebound.Root != l.rootPath || rebound.Revision != loaded.revision || !bytes.Equal(rebound.Graphs[3], saved.Graphs[3]) {
		t.Fatal("offline cache was not safely adopted", err)
	}
	newID := insert(vector{.6, .8, 0}, nil)
	a.personIndex = nil // new vectors arrived after the last persisted snapshot
	loaded, err = a.loadPersonHNSW(ctx)
	if err != nil || len(loaded.vectors[newID]) != 1 {
		t.Fatal("restart lost appended representative", err)
	}
	loadedRevision := loaded.revision
	if _, err = a.db.Exec("UPDATE people SET merged_into=? WHERE id=?", other, alias); err != nil {
		t.Fatal(err)
	}
	changed, err := a.loadPersonHNSW(ctx)
	if err != nil || changed.revision == loadedRevision || len(changed.vectors[other]) != 2 || len(changed.vectors[id]) != 1 {
		t.Fatal("merge invalidation", err)
	}
	if _, err = a.db.Exec("UPDATE people SET embedding=? WHERE id=?", vector{.8, .6, 0}.blob(), id); err != nil {
		t.Fatal(err)
	}
	changed, err = a.loadPersonHNSW(ctx)
	if err != nil || changed.vectors[id][0][0] != .8 {
		t.Fatal("embedding invalidation", err)
	}
	if _, err = a.db.Exec("DELETE FROM people WHERE id=?", alias); err != nil {
		t.Fatal(err)
	}
	changed, err = a.loadPersonHNSW(ctx)
	if err != nil || len(changed.vectors[other]) != 1 {
		t.Fatal("delete invalidation", err)
	}
	if err = os.WriteFile(path, []byte("broken cache"), 0600); err != nil {
		t.Fatal(err)
	}
	a.personIndex = nil
	if _, err = a.loadPersonHNSW(ctx); err != nil {
		t.Fatal("corrupt cache should rebuild", err)
	}
	if _, err = readPersonHNSW(path); err != nil {
		t.Fatal("rebuilt cache", err)
	}
	// A cancelled request cannot publish a partially built snapshot.
	if _, err = a.db.Exec("UPDATE people SET embedding=? WHERE id=?", vector{1, 0, 0}.blob(), id); err != nil {
		t.Fatal(err)
	}
	cancelled, cancel := context.WithCancel(ctx)
	cancel()
	if _, err = a.loadPersonHNSW(cancelled); err == nil {
		t.Fatal("ignored cancellation")
	}
	if _, err = a.loadPersonHNSW(ctx); err != nil {
		t.Fatal(err)
	}
}

func TestPersonHNSWExpandsAfterExclusions(t *testing.T) {
	l, s, _ := fixture(t)
	a := l.ai
	ctx := context.Background()
	first, second := testJob(t, l, "a.jpg"), testJob(t, l, "b.jpg")
	if err := a.save(ctx, first, syntheticPrediction()); err != nil {
		t.Fatal(err)
	}
	prediction := syntheticPrediction()
	prediction.Faces[0].Embedding = []byte(`"[0.5,0.866,0]"`)
	if err := a.save(ctx, second, prediction); err != nil {
		t.Fatal(err)
	}
	var source int64
	if err := a.db.QueryRow("SELECT person_id FROM faces WHERE image_id=?", first.ID).Scan(&source); err != nil {
		t.Fatal(err)
	}
	tx, err := a.db.Begin()
	if err != nil {
		t.Fatal(err)
	}
	// Every close group is dismissed except the last one. A small ANN search
	// cannot satisfy this request without expanding beyond the first shortlist.
	if _, err = tx.Exec("DELETE FROM faces WHERE image_id=?", second.ID); err != nil {
		t.Fatal(err)
	}
	var wanted int64
	for i := 0; i < 1200; i++ {
		r, e := tx.Exec("INSERT INTO people(model,embedding) VALUES(?,?)", l.config.ML.FaceModel, vector{1, 0, 0}.blob())
		if e != nil {
			t.Fatal(e)
		}
		id, _ := r.LastInsertId()
		wanted = id
		if _, err = tx.Exec("INSERT INTO faces(image_id,person_id,box,score,width,height) VALUES(?,?,'{}',1,100,100)", second.ID, id); err != nil {
			t.Fatal(err)
		}
		if i < 1199 {
			if _, err = tx.Exec("INSERT INTO person_dismissals VALUES(?,?)", source, id); err != nil {
				t.Fatal(err)
			}
		}
	}
	if err = tx.Commit(); err != nil {
		t.Fatal(err)
	}
	path := fmt.Sprintf("/api/people/%d/suggestions?limit=25&min_score=0", source)
	var result struct {
		Items       []personSuggestion
		SearchIndex string `json:"search_index"`
	}
	if getJSON(t, s, path, &result) != 200 || len(result.Items) != 1 || result.Items[0].ID != wanted || result.SearchIndex != "hnsw" {
		t.Fatalf("filtered shortlist: %+v", result)
	}
	// Eligibility changes do not need to rebuild the vector index.
	index := a.personIndex
	postTest(t, s, "/api/curation/"+second.ID, `{"hidden":true}`)
	if getJSON(t, s, path, &result) != 200 || len(result.Items) != 0 || a.personIndex != index {
		t.Fatal("hidden group leaked or rebuilt index")
	}
	postTest(t, s, "/api/curation/"+second.ID, `{"hidden":false}`)
	if err = l.retirePath(ctx, "b.jpg"); err != nil {
		t.Fatal(err)
	}
	if getJSON(t, s, path, &result) != 200 || len(result.Items) != 0 {
		t.Fatal("trashed group leaked")
	}
}

// Opt-in read-only production snapshot check; all graph/cache writes go to the
// temporary fixture. No production catalog, assignments, or cache is modified.
func TestPersonHNSWLiveSnapshot(t *testing.T) {
	path := os.Getenv("HOMEIMAGES_TEST_PEOPLE_DB")
	if path == "" {
		t.Skip("set HOMEIMAGES_TEST_PEOPLE_DB to benchmark a read-only snapshot")
	}
	cache := os.Getenv("HOMEIMAGES_TEST_PEOPLE_CACHE")
	if cache != "" {
		if saved, err := readPersonHNSW(cache); err == nil && saved.Version == 3 {
			if index, err := restorePersonHNSW(saved); err == nil {
				checkPersonHNSWLiveRecall(t, index)
				return
			}
		}
	}
	l, _, _ := fixture(t)
	uriPath := filepath.ToSlash(path)
	if filepath.VolumeName(path) != "" {
		uriPath = "/" + uriPath
	}
	u := url.URL{Scheme: "file", Path: uriPath, RawQuery: "mode=ro"}
	db := openSQLite(u.String(), nil, true)
	defer db.Close()
	rows, err := db.Query("SELECT id,model,embedding,merged_into FROM people ORDER BY id")
	if err != nil {
		t.Fatal(err)
	}
	tx, err := l.ai.db.Begin()
	if err != nil {
		t.Fatal(err)
	}
	for rows.Next() {
		var id int64
		var model string
		var blob []byte
		var merged *int64
		if err = rows.Scan(&id, &model, &blob, &merged); err != nil {
			t.Fatal(err)
		}
		if _, err = tx.Exec("INSERT INTO people(id,model,embedding,merged_into) VALUES(?,?,?,?)", id, model, blob, merged); err != nil {
			t.Fatal(err)
		}
	}
	if err = rows.Err(); err != nil {
		t.Fatal(err)
	}
	rows.Close()
	if err = tx.Commit(); err != nil {
		t.Fatal(err)
	}
	started := time.Now()
	index, err := l.ai.loadPersonHNSW(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("build %s, %d representatives, %d groups", time.Since(started), len(index.groups), len(index.vectors))
	if cache != "" {
		saved, err := readPersonHNSW(filepath.Join(l.config.DataDir, "people.hnsw"))
		if err != nil {
			t.Fatal(err)
		}
		if err = writePersonHNSW(cache, saved); err != nil {
			t.Fatal(err)
		}
	}
	checkPersonHNSWLiveRecall(t, index)
}

func checkPersonHNSWLiveRecall(t *testing.T, index *personHNSW) {
	t.Helper()
	ids := sortedPersonIDs(func() map[int64]bool {
		m := map[int64]bool{}
		for id := range index.vectors {
			m[id] = true
		}
		return m
	}())
	if len(ids) < 10 {
		t.Fatal("snapshot has too few groups")
	}
	ids = ids[:10]
	if raw := os.Getenv("HOMEIMAGES_TEST_PEOPLE_SOURCE"); raw != "" {
		id, err := strconv.ParseInt(raw, 10, 64)
		if err != nil || len(index.vectors[id]) == 0 {
			t.Fatal("invalid benchmark source", raw)
		}
		ids = []int64{id}
	}
	for _, source := range ids {
		started := time.Now()
		candidates, err := index.candidates(context.Background(), source, 1024)
		if err != nil {
			t.Fatal(err)
		}
		for k := 2048; len(candidates) < 64 && k < 2*len(index.groups); k *= 2 {
			candidates, err = index.candidates(context.Background(), source, min(k, len(index.groups)))
			if err != nil {
				t.Fatal(err)
			}
		}
		annTime := time.Since(started)
		type scored struct {
			id    int64
			score float32
		}
		var exact []scored
		started = time.Now()
		for id, vs := range index.vectors {
			if id == source {
				continue
			}
			var score float32
			for _, v := range index.vectors[source] {
				for _, other := range vs {
					score = max(score, similarity(v, other))
				}
			}
			exact = append(exact, scored{id, score})
		}
		sort.Slice(exact, func(i, j int) bool { return exact[i].score > exact[j].score })
		var hits int
		for _, p := range exact[:25] {
			if candidates[p.id] {
				hits++
			}
		}
		t.Logf("source %d: ANN %s, exhaustive %s, top25 recall %d/25, %d candidates", source, annTime, time.Since(started), hits, len(candidates))
		if hits < 23 {
			t.Errorf("source %d low recall %d/25", source, hits)
		}
	}
}

func TestPersonHNSWRecallAndDimensions(t *testing.T) {
	l, _, _ := fixture(t)
	a := l.ai
	rng := rand.New(rand.NewSource(42))
	tx, err := a.db.Begin()
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 800; i++ {
		v := make(vector, 32)
		var norm float64
		for j := range v {
			v[j] = float32(rng.NormFloat64())
			norm += float64(v[j]) * float64(v[j])
		}
		for j := range v {
			v[j] /= float32(math.Sqrt(norm))
		}
		if _, err = tx.Exec("INSERT INTO people(model,embedding) VALUES(?,?)", l.config.ML.FaceModel, v.blob()); err != nil {
			t.Fatal(err)
		}
	}
	// Legacy embeddings of another dimension cannot panic the graph.
	if _, err = tx.Exec("INSERT INTO people(model,embedding) VALUES(?,?)", l.config.ML.FaceModel, vector{1, 0, 0}.blob()); err != nil {
		t.Fatal(err)
	}
	if err = tx.Commit(); err != nil {
		t.Fatal(err)
	}
	index, err := a.loadPersonHNSW(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	hits := 0
	for source := int64(1); source <= 10; source++ {
		candidates, err := index.candidates(context.Background(), source, 64)
		if err != nil {
			t.Fatal(err)
		}
		type scored struct {
			id    int64
			score float32
		}
		var exact []scored
		for id, v := range index.vectors {
			if id != source {
				exact = append(exact, scored{id, similarity(index.vectors[source][0], v[0])})
			}
		}
		sort.Slice(exact, func(i, j int) bool { return exact[i].score > exact[j].score })
		for _, p := range exact[:10] {
			if candidates[p.id] {
				hits++
			}
		}
	}
	if hits < 95 {
		t.Fatalf("top-10 recall %d/100", hits)
	}
	// A heavily merged person must retain all appearances without running a
	// separate wide ANN search for every alias or sampling away rare matches.
	if _, err = a.db.Exec("UPDATE people SET merged_into=1 WHERE id BETWEEN 2 AND 100"); err != nil {
		t.Fatal(err)
	}
	index, err = a.loadPersonHNSW(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	candidates, err := index.candidates(context.Background(), 1, 128)
	if err != nil {
		t.Fatal(err)
	}
	type scored struct {
		id    int64
		score float32
	}
	var exact []scored
	for id, vs := range index.vectors {
		if id == 1 {
			continue
		}
		var score float32
		for _, v := range index.vectors[1] {
			for _, other := range vs {
				score = max(score, similarity(v, other))
			}
		}
		exact = append(exact, scored{id, score})
	}
	sort.Slice(exact, func(i, j int) bool { return exact[i].score > exact[j].score })
	hits = 0
	for _, p := range exact[:25] {
		if candidates[p.id] {
			hits++
		}
	}
	if hits < 24 {
		t.Fatalf("merged group top-25 recall %d/25", hits)
	}
}
