package main

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"
)

func (l *Library) ensureDuplicateBatchSchema() error {
	_, err := l.db.Exec(`CREATE TABLE IF NOT EXISTS duplicate_batches(id TEXT PRIMARY KEY,action TEXT NOT NULL,state TEXT NOT NULL,summary TEXT NOT NULL,created_at TEXT NOT NULL,updated_at TEXT NOT NULL);
 CREATE TABLE IF NOT EXISTS duplicate_batch_items(batch_id TEXT NOT NULL,sequence INTEGER NOT NULL,remove_id TEXT NOT NULL,keep_id TEXT NOT NULL,file_hash TEXT NOT NULL,remove_path TEXT NOT NULL,keep_path TEXT NOT NULL,size_bytes INTEGER NOT NULL,token TEXT NOT NULL DEFAULT '',state TEXT NOT NULL DEFAULT 'pending',error TEXT NOT NULL DEFAULT '',PRIMARY KEY(batch_id,sequence));
 CREATE INDEX IF NOT EXISTS duplicate_batch_remove ON duplicate_batch_items(batch_id,remove_id,state);
 CREATE INDEX IF NOT EXISTS duplicate_batch_keep ON duplicate_batch_items(batch_id,keep_id,state);
 CREATE INDEX IF NOT EXISTS duplicate_batch_token ON duplicate_batch_items(token);
 UPDATE duplicate_batches SET state='interrupted' WHERE state='running';
 UPDATE duplicate_batches SET state='cancelled' WHERE state='cancelling';`)
	return err
}
func batchID() (string, error) {
	b := make([]byte, 16)
	_, err := rand.Read(b)
	return hex.EncodeToString(b), err
}
func (l *Library) previewDirectoryCleanup(w http.ResponseWriter, r *http.Request) error {
	var body struct {
		Source string `json:"source"`
		Keep   string `json:"keep"`
	}
	if json.NewDecoder(r.Body).Decode(&body) != nil {
		return bad(400, "Invalid request")
	}
	source, err := cleanOverlapDir(body.Source)
	if err != nil {
		return err
	}
	keep, err := cleanOverlapDir(body.Keep)
	if err != nil {
		return err
	}
	plan, err := l.buildDirectoryPlan(r.Context(), source, keep)
	if err != nil {
		return err
	}
	if len(plan.Items) == 0 {
		return bad(400, "There are no exact duplicates to move to Trash between these trees")
	}
	id, err := batchID()
	if err != nil {
		return err
	}
	items := plan.Items
	plan.Items = nil
	summary, err := json.Marshal(plan)
	if err != nil {
		return err
	}
	now := time.Now().UTC().Format(time.RFC3339)
	tx, err := l.db.BeginTx(r.Context(), nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err = tx.ExecContext(r.Context(), "INSERT INTO duplicate_batches VALUES(?, 'quarantine','preview',?,?,?)", id, string(summary), now, now); err != nil {
		return err
	}
	stmt, err := tx.PrepareContext(r.Context(), "INSERT INTO duplicate_batch_items(batch_id,sequence,remove_id,keep_id,file_hash,remove_path,keep_path,size_bytes) VALUES(?,?,?,?,?,?,?,?)")
	if err != nil {
		return err
	}
	defer stmt.Close()
	for i, item := range items {
		if _, err = stmt.ExecContext(r.Context(), id, i, item.RemoveID, item.KeepID, item.Hash, item.RemovePath, item.KeepPath, item.Size); err != nil {
			return err
		}
	}
	if err = tx.Commit(); err != nil {
		return err
	}
	return writeJSON(w, 200, map[string]any{"id": id, "summary": plan, "count": len(items), "sample": items[:min(100, len(items))]})
}
func (l *Library) duplicateBatchStatus(w http.ResponseWriter, r *http.Request) error {
	id := r.PathValue("batch")
	var action, state, raw, created, updated string
	if err := l.read.QueryRowContext(r.Context(), "SELECT action,state,summary,created_at,updated_at FROM duplicate_batches WHERE id=?", id).Scan(&action, &state, &raw, &created, &updated); err != nil {
		return bad(404, "Cleanup job not found")
	}
	var summary any
	if err := json.Unmarshal([]byte(raw), &summary); err != nil {
		return err
	}
	rows, err := l.read.QueryContext(r.Context(), "SELECT state,count(*),coalesce(sum(size_bytes),0) FROM duplicate_batch_items WHERE batch_id=? GROUP BY state", id)
	if err != nil {
		return err
	}
	counts := map[string]int{}
	var bytes int64
	total := 0
	for rows.Next() {
		var itemState string
		var n int
		var size int64
		if err = rows.Scan(&itemState, &n, &size); err != nil {
			break
		}
		counts[itemState] = n
		total += n
		if itemState == "done" {
			bytes += size
		}
	}
	rowErr := rows.Err()
	rows.Close()
	if err != nil {
		return err
	}
	if rowErr != nil {
		return rowErr
	}
	errors := []map[string]string{}
	rows, err = l.read.QueryContext(r.Context(), "SELECT remove_path,error FROM duplicate_batch_items WHERE batch_id=? AND state='error' ORDER BY sequence LIMIT 50", id)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var path, message string
		if err = rows.Scan(&path, &message); err != nil {
			return err
		}
		errors = append(errors, map[string]string{"path": path, "message": message})
	}
	if err = rows.Err(); err != nil {
		return err
	}
	rows.Close()
	var trashCount int
	if err = l.read.QueryRowContext(r.Context(), "SELECT count(*) FROM duplicate_batch_items n JOIN duplicate_quarantine q ON q.token=n.token WHERE n.batch_id=? AND n.state='done'", id).Scan(&trashCount); err != nil {
		return err
	}
	return writeJSON(w, 200, map[string]any{"id": id, "action": action, "state": state, "summary": summary, "trash_count": trashCount, "counts": counts, "total": total, "completed_bytes": bytes, "errors": errors, "created_at": created, "updated_at": updated})
}
func (l *Library) duplicateBatchList(w http.ResponseWriter, r *http.Request) error {
	filter := "state!='preview'"
	if r.URL.Query().Get("view") == "trash" {
		filter = "action='quarantine' AND EXISTS (SELECT 1 FROM duplicate_batch_items i JOIN duplicate_quarantine q ON q.token=i.token WHERE i.batch_id=duplicate_batches.id AND i.state='done')"
	} else if r.URL.Query().Get("view") == "tasks" {
		filter = "state IN ('running','cancelling') OR (state IN ('completed','cancelled','interrupted') AND EXISTS (SELECT 1 FROM duplicate_batch_items i WHERE i.batch_id=duplicate_batches.id AND i.state IN ('error','pending')))"
	}
	rows, err := l.read.QueryContext(r.Context(), `SELECT id,action,state,summary,created_at,
 (SELECT count(*) FROM duplicate_batch_items i WHERE i.batch_id=duplicate_batches.id),
 (SELECT count(*) FROM duplicate_batch_items i WHERE i.batch_id=duplicate_batches.id AND i.state='done'),
 (SELECT count(*) FROM duplicate_batch_items i WHERE i.batch_id=duplicate_batches.id AND i.state='error'),
 (SELECT coalesce(sum(size_bytes),0) FROM duplicate_batch_items i WHERE i.batch_id=duplicate_batches.id AND i.state='done'),
 (SELECT count(*) FROM duplicate_batch_items i WHERE i.batch_id=duplicate_batches.id AND i.state='pending'),
 (SELECT count(*) FROM duplicate_batch_items i JOIN duplicate_quarantine q ON q.token=i.token WHERE i.batch_id=duplicate_batches.id AND i.state='done')
 FROM duplicate_batches WHERE `+filter+` ORDER BY created_at DESC`)
	if err != nil {
		return err
	}
	defer rows.Close()
	items := []map[string]any{}
	for rows.Next() {
		var id, action, state, raw, created string
		var total, done, failed, pending, trashCount int
		var completedBytes int64
		if err = rows.Scan(&id, &action, &state, &raw, &created, &total, &done, &failed, &completedBytes, &pending, &trashCount); err != nil {
			return err
		}
		var summary any
		if err = json.Unmarshal([]byte(raw), &summary); err != nil {
			return err
		}
		items = append(items, map[string]any{"id": id, "action": action, "state": state, "summary": summary, "created_at": created, "total": total, "done": done, "failed": failed, "completed_bytes": completedBytes, "pending": pending, "trash_count": trashCount})
	}
	if err = rows.Err(); err != nil {
		return err
	}
	return writeJSON(w, 200, map[string]any{"items": items})
}
func (l *Library) startDuplicateBatch(w http.ResponseWriter, r *http.Request) error {
	var body struct {
		ID     string `json:"id"`
		Action string `json:"action"`
	}
	if json.NewDecoder(r.Body).Decode(&body) != nil {
		return bad(400, "Invalid request")
	}
	l.duplicateJobMu.Lock()
	defer l.duplicateJobMu.Unlock()
	if l.duplicateClosing {
		return bad(503, "Service is shutting down")
	}
	if _, running := l.duplicateJobs[body.ID]; running {
		return writeJSON(w, 200, map[string]any{"id": body.ID})
	}
	var action, state, summary string
	if err := l.db.QueryRowContext(r.Context(), "SELECT action,state,summary FROM duplicate_batches WHERE id=?", body.ID).Scan(&action, &state, &summary); err != nil {
		return bad(404, "Review a cleanup plan first")
	}
	id := body.ID
	if body.Action == "restore" || body.Action == "purge" {
		if action != "quarantine" || state == "preview" || state == "running" || state == "cancelling" {
			return bad(409, "Choose a finished or cancelled Trash job")
		}
		var err error
		id, err = batchID()
		if err != nil {
			return err
		}
		now := time.Now().UTC().Format(time.RFC3339Nano)
		tx, err := l.db.BeginTx(r.Context(), nil)
		if err != nil {
			return err
		}
		defer tx.Rollback()
		if _, err = tx.ExecContext(r.Context(), "INSERT INTO duplicate_batches VALUES(?,?,'preview',?,?,?)", id, body.Action, summary, now, now); err != nil {
			return err
		}
		result, err := tx.ExecContext(r.Context(), `INSERT INTO duplicate_batch_items(batch_id,sequence,remove_id,keep_id,file_hash,remove_path,keep_path,size_bytes,token) SELECT ?,n.sequence,n.remove_id,n.keep_id,n.file_hash,n.remove_path,n.keep_path,n.size_bytes,n.token FROM duplicate_batch_items n JOIN duplicate_quarantine q ON q.token=n.token WHERE n.batch_id=? AND n.state='done' AND n.token!=''`, id, body.ID)
		if err != nil {
			return err
		}
		n, err := result.RowsAffected()
		if err != nil {
			return err
		}
		if n == 0 {
			return bad(400, "This job has no copies in Trash")
		}
		if err = tx.Commit(); err != nil {
			return err
		}
		action = body.Action
		state = "preview"
	} else if body.Action != "" && body.Action != "resume" && body.Action != "quarantine" {
		return bad(400, "Unknown cleanup action")
	}
	// Jobs sharing any source or retained file must not race each other.
	var conflicts int
	if err := l.db.QueryRowContext(r.Context(), `SELECT EXISTS(
        SELECT 1 FROM duplicate_batches active
        CROSS JOIN duplicate_batch_items b ON b.batch_id=active.id AND b.state='pending'
        CROSS JOIN duplicate_batch_items a INDEXED BY duplicate_batch_remove ON a.batch_id=? AND a.remove_id=b.remove_id AND a.state IN ('pending','error')
        WHERE active.id!=? AND active.state IN ('running','cancelling')
        UNION ALL
        SELECT 1 FROM duplicate_batches active
        CROSS JOIN duplicate_batch_items b ON b.batch_id=active.id AND b.state='pending'
        CROSS JOIN duplicate_batch_items a INDEXED BY duplicate_batch_remove ON a.batch_id=? AND a.remove_id=b.keep_id AND a.state IN ('pending','error')
        WHERE active.id!=? AND active.state IN ('running','cancelling')
        UNION ALL
        SELECT 1 FROM duplicate_batches active
        CROSS JOIN duplicate_batch_items b ON b.batch_id=active.id AND b.state='pending'
        CROSS JOIN duplicate_batch_items a INDEXED BY duplicate_batch_keep ON a.batch_id=? AND a.keep_id=b.remove_id AND a.state IN ('pending','error')
        WHERE active.id!=? AND active.state IN ('running','cancelling'))`, id, id, id, id, id, id).Scan(&conflicts); err != nil {
		return err
	}
	if conflicts != 0 {
		return bad(409, "Another cleanup job uses the same files; wait for it to finish")
	}
	if state != "preview" && state != "cancelled" && state != "interrupted" && state != "completed" {
		return bad(409, "This job cannot be started")
	}
	if _, err := l.db.ExecContext(r.Context(), "UPDATE duplicate_batch_items SET state='pending',error='' WHERE batch_id=? AND state='error'", id); err != nil {
		return err
	}
	if _, err := l.db.ExecContext(r.Context(), "UPDATE duplicate_batches SET state='running',updated_at=? WHERE id=?", time.Now().UTC().Format(time.RFC3339Nano), id); err != nil {
		return err
	}
	l.launchDuplicateBatch(id, action)
	return writeJSON(w, 200, map[string]any{"id": id})
}

// launchDuplicateBatch requires duplicateJobMu to be held.
func (l *Library) launchDuplicateBatch(id, action string) {
	ctx, cancel := context.WithCancel(context.Background())
	if l.duplicateJobs == nil {
		l.duplicateJobs = make(map[string]context.CancelFunc)
	}
	l.duplicateJobs[id] = cancel
	l.duplicateJobWG.Add(1)
	go l.runDuplicateBatch(ctx, id, action)
}
func (l *Library) cancelDuplicateBatch(w http.ResponseWriter, r *http.Request) error {
	var body struct {
		ID string `json:"id"`
	}
	if json.NewDecoder(r.Body).Decode(&body) != nil {
		return bad(400, "Invalid request")
	}
	l.duplicateJobMu.Lock()
	defer l.duplicateJobMu.Unlock()
	if cancel := l.duplicateJobs[body.ID]; cancel != nil {
		if _, err := l.db.ExecContext(r.Context(), "UPDATE duplicate_batches SET state='cancelling' WHERE id=?", body.ID); err != nil {
			return err
		}
		cancel()
	}
	return writeJSON(w, 200, map[string]any{"ok": true})
}

type batchDiscardWriter struct{ header http.Header }

func (w *batchDiscardWriter) Header() http.Header {
	if w.header == nil {
		w.header = http.Header{}
	}
	return w.header
}
func (w *batchDiscardWriter) Write(b []byte) (int, error) { return len(b), nil }
func (w *batchDiscardWriter) WriteHeader(int)             {}
func (l *Library) runDuplicateBatch(ctx context.Context, id, action string) {
	defer l.duplicateJobWG.Done()
	defer func() {
		l.duplicateJobMu.Lock()
		delete(l.duplicateJobs, id)
		l.duplicateJobMu.Unlock()
	}()
	state := "completed"
	defer func() {
		if l.ai != nil {
			l.ai.selectionRevision.Add(1)
		}
		if ctx.Err() != nil {
			state = "cancelled"
			l.duplicateJobMu.Lock()
			closing := l.duplicateClosing
			l.duplicateJobMu.Unlock()
			var stored string
			if closing && action == "quarantine" && l.db.QueryRow("SELECT state FROM duplicate_batches WHERE id=?", id).Scan(&stored) == nil && stored != "cancelling" {
				state = "interrupted"
			}
		}
		_, _ = l.db.Exec("UPDATE duplicate_batches SET state=?,updated_at=? WHERE id=?", state, time.Now().UTC().Format(time.RFC3339Nano), id)
	}()
	for ctx.Err() == nil {
		var sequence int
		var item duplicatePlanItem
		var token string
		err := l.db.QueryRowContext(ctx, `SELECT sequence,remove_id,keep_id,file_hash,remove_path,keep_path,size_bytes,token FROM duplicate_batch_items WHERE batch_id=? AND state='pending' ORDER BY sequence LIMIT 1`, id).Scan(&sequence, &item.RemoveID, &item.KeepID, &item.Hash, &item.RemovePath, &item.KeepPath, &item.Size, &token)
		if err != nil {
			if !errors.Is(err, sql.ErrNoRows) {
				state = "interrupted"
			}
			return
		}
		request, _ := http.NewRequestWithContext(ctx, http.MethodPost, "http://localhost/internal-duplicate-job", nil)
		if action == "quarantine" {
			// Reconnect a completed filesystem move after an interrupted process.
			var recovered, dest string
			recoveryErr := l.db.QueryRowContext(ctx, "SELECT token,quarantine_path FROM duplicate_quarantine WHERE image_id=? AND keep_id=? AND file_hash=? ORDER BY created_at DESC LIMIT 1", item.RemoveID, item.KeepID, item.Hash).Scan(&recovered, &dest)
			if recoveryErr == nil {
				if _, e := l.root.Stat(filepath.FromSlash(dest)); e == nil {
					token = recovered
					l.scanMu.Lock()
					if _, e := l.root.Lstat(filepath.FromSlash(item.RemovePath)); errors.Is(e, os.ErrNotExist) {
						err = l.retirePath(ctx, item.RemovePath)
					}
					l.scanMu.Unlock()
				} else {
					recovered = ""
				}
			}
			if recovered == "" {
				remove, e := l.get(ctx, item.RemoveID)
				if e == nil && (remove.ID != item.RemoveID || remove.Path != item.RemovePath) {
					e = bad(409, "Source path changed after preview")
				}
				keep, ke := l.get(ctx, item.KeepID)
				if ke == nil && (keep.ID != item.KeepID || keep.Path != item.KeepPath) {
					ke = bad(409, "Retained path changed after preview")
				}
				if e != nil {
					err = e
				} else if ke != nil {
					err = ke
				} else {
					var result map[string]any
					result, err = l.quarantinePairExpected(request, item.KeepID, item.RemoveID, item.Hash, item.KeepPath, item.RemovePath)
					if err == nil {
						token, _ = result["token"].(string)
					}
				}
			}
		} else {
			request.Body = http.NoBody
			payload, _ := json.Marshal(map[string]string{"token": token})
			request.Body = ioBody(string(payload))
			writer := &batchDiscardWriter{}
			if action == "restore" {
				err = l.restoreDuplicate(writer, request)
			} else if action == "purge" {
				err = l.purgeDuplicate(writer, request)
			} else {
				err = fmt.Errorf("unknown job action")
			}
		}
		if ctx.Err() != nil && err != nil {
			return
		}
		itemState, message := "done", ""
		if err != nil {
			itemState = "error"
			message = err.Error()
		}
		if _, err = l.db.Exec("UPDATE duplicate_batch_items SET state=?,error=?,token=? WHERE batch_id=? AND sequence=?", itemState, message, token, id, sequence); err != nil {
			state = "interrupted"
			return
		}
		if !waitContext(ctx, 20*time.Millisecond) {
			return
		}
	}
}
func ioBody(s string) io.ReadCloser { return io.NopCloser(strings.NewReader(s)) }

// resumeInterruptedTrashJobs restarts only work interrupted by shutdown or a crash.
func (l *Library) resumeInterruptedTrashJobs() error {
	rows, err := l.db.Query("SELECT id FROM duplicate_batches WHERE action='quarantine' AND state='interrupted' ORDER BY created_at,id")
	if err != nil {
		return err
	}
	var ids []string
	for rows.Next() {
		var id string
		if err = rows.Scan(&id); err != nil {
			break
		}
		ids = append(ids, id)
	}
	if err == nil {
		err = rows.Err()
	}
	rows.Close()
	if err != nil {
		return err
	}
	l.duplicateJobMu.Lock()
	defer l.duplicateJobMu.Unlock()
	for _, id := range ids {
		if _, err = l.db.Exec("UPDATE duplicate_batches SET state='running',updated_at=? WHERE id=? AND state='interrupted'", time.Now().UTC().Format(time.RFC3339Nano), id); err != nil {
			return err
		}
		l.launchDuplicateBatch(id, "quarantine")
	}
	return nil
}
