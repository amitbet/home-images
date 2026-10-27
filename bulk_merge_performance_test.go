package main

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"
)

func TestLargeBulkMergeSingleHistoryWriteAndUndo(t *testing.T) {
	l, s, _ := fixture(t)
	j := testJob(t, l, "a.jpg")
	if err := l.ai.save(context.Background(), j, syntheticPrediction()); err != nil {
		t.Fatal(err)
	}
	var target int64
	l.ai.db.QueryRow("SELECT person_id FROM faces WHERE image_id=?", j.ID).Scan(&target)
	// Realistic unrelated population: scanning this table per source used to
	// repeatedly traverse large embedding records instead of an alias index.
	if _, err := l.ai.db.Exec(`WITH RECURSIVE n(x) AS (VALUES(1) UNION ALL SELECT x+1 FROM n WHERE x<20000) INSERT INTO people(name,model,embedding) SELECT 'unrelated',?,zeroblob(2048) FROM n`, l.config.ML.FaceModel); err != nil {
		t.Fatal(err)
	}
	sources := []int64{}
	aliases := map[int64]int64{}
	for n := 0; n < 64; n++ {
		result, err := l.ai.db.Exec("INSERT INTO people(name,model,embedding) VALUES(?,?,zeroblob(2048))", fmt.Sprintf("source-%d", n), l.config.ML.FaceModel)
		if err != nil {
			t.Fatal(err)
		}
		source, _ := result.LastInsertId()
		sources = append(sources, source)
		result, err = l.ai.db.Exec("INSERT INTO people(model,embedding,merged_into) VALUES(?,zeroblob(2048),?)", l.config.ML.FaceModel, source)
		if err != nil {
			t.Fatal(err)
		}
		alias, _ := result.LastInsertId()
		aliases[source] = alias
		if _, err = l.ai.db.Exec("INSERT INTO faces(image_id,person_id,box,score,width,height) VALUES(?,?,?,.9,100,100)", j.ID, source, fmt.Sprint(n)); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := l.ai.db.Exec(`CREATE TABLE history_writes(value INTEGER);CREATE TRIGGER history_insert AFTER INSERT ON ai_settings WHEN new.key='merge_undo' BEGIN INSERT INTO history_writes VALUES(1);END;CREATE TRIGGER history_update AFTER UPDATE ON ai_settings WHEN new.key='merge_undo' BEGIN INSERT INTO history_writes VALUES(1);END;`); err != nil {
		t.Fatal(err)
	}
	body, _ := json.Marshal(map[string]any{"sources": sources})
	start := time.Now()
	if postTest(t, s, fmt.Sprintf("/api/people/%d/merge-many", target), string(body)) != 200 {
		t.Fatal("merge failed")
	}
	t.Logf("Merged 64 groups among 20,000 unrelated people in %s", time.Since(start))
	// The same request can be retried after a timeout without changing history.
	if postTest(t, s, fmt.Sprintf("/api/people/%d/merge-many", target), string(body)) != 200 {
		t.Fatal("idempotent retry failed")
	}
	var writes int
	l.ai.db.QueryRow("SELECT count(*) FROM history_writes").Scan(&writes)
	if writes != 1 {
		t.Fatal("undo history must be serialized/stored once per batch", writes)
	}
	var detail string
	rows, err := l.ai.db.Query("EXPLAIN QUERY PLAN SELECT id FROM people WHERE merged_into=?", target)
	if err != nil {
		t.Fatal(err)
	}
	indexed := false
	for rows.Next() {
		var a, b, c int
		rows.Scan(&a, &b, &c, &detail)
		indexed = indexed || strings.Contains(detail, "people_merged_into")
	}
	rows.Close()
	if !indexed {
		t.Fatal("alias lookup scans people", detail)
	}
	for n := len(sources) - 1; n >= 0; n-- {
		var history struct {
			Available      bool
			Source, Target int64
		}
		if getJSON(t, s, "/api/people/merge-history", &history) != 200 || !history.Available || history.Source != sources[n] || history.Target != target {
			t.Fatal("lost undo entry", n, history)
		}
		payload, _ := json.Marshal(map[string]int64{"source": history.Source, "target": history.Target})
		if postTest(t, s, "/api/people/undo", string(payload)) != 200 {
			t.Fatal("undo failed", n)
		}
		var name string
		var active bool
		l.ai.db.QueryRow("SELECT name,merged_into IS NULL FROM people WHERE id=?", sources[n]).Scan(&name, &active)
		if !active || name != fmt.Sprintf("source-%d", n) {
			t.Fatal("source not restored", n, name, active)
		}
		var parent int64
		l.ai.db.QueryRow("SELECT merged_into FROM people WHERE id=?", aliases[sources[n]]).Scan(&parent)
		if parent != sources[n] {
			t.Fatal("alias not restored", n, parent)
		}
	}
}
