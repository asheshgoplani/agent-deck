package trace

import (
	"bufio"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"
)

// IngestResult reports what one Ingest call did.
type IngestResult struct {
	Files     int // spool files read
	Lines     int // lines read
	Applied   int // records applied (new)
	Duplicate int // records already in trace.db (idempotent replay)
	Bad       int // undecodable lines skipped
	Removed   int // spool files removed after commit
}

// IngestOptions tunes Ingest. The zero value is production behaviour.
type IngestOptions struct {
	// Grace keeps a rotated spool file on disk until it has been quiet this
	// long, so a hook that opened the file just before rotation and writes
	// after the read is picked up by the next ingest (replay is idempotent).
	// Zero means the default (5s); negative means remove immediately.
	Grace time.Duration
	Now   func() time.Time
}

const defaultGrace = 5 * time.Second

// Ingest moves spooled records under profileDir into s in one transaction.
// It rotates the live spool (rename, so appenders start a fresh file), reads
// every rotated file, applies the records ordered by time, and removes the
// files it fully committed. Each record's id is the events primary key, so
// replaying a file applies nothing twice.
func (s *Store) Ingest(profileDir string, opts IngestOptions) (IngestResult, error) {
	var res IngestResult
	now := time.Now
	if opts.Now != nil {
		now = opts.Now
	}
	grace := opts.Grace
	if grace == 0 {
		grace = defaultGrace
	}
	dir := SpoolDir(profileDir)
	live := SpoolPath(profileDir)
	if st, err := os.Stat(live); err == nil && st.Size() > 0 {
		rotated := filepath.Join(dir, "spool-"+strconv.FormatInt(now().UnixNano(), 10)+".jsonl")
		if err := os.Rename(live, rotated); err != nil {
			return res, fmt.Errorf("trace: rotate spool: %w", err)
		}
	}
	files, err := filepath.Glob(filepath.Join(dir, "spool-*.jsonl"))
	if err != nil {
		return res, err
	}
	sort.Strings(files)

	type fileState struct {
		path  string
		size  int64
		mtime time.Time
	}
	var states []fileState
	var recs []SpoolRecord
	for _, p := range files {
		st, err := os.Stat(p)
		if err != nil {
			continue
		}
		f, err := os.Open(p)
		if err != nil {
			continue
		}
		sc := bufio.NewScanner(f)
		sc.Buffer(make([]byte, 0, 64<<10), 1<<20)
		for sc.Scan() {
			line := strings.TrimSpace(sc.Text())
			if line == "" {
				continue
			}
			res.Lines++
			var r SpoolRecord
			if json.Unmarshal([]byte(line), &r) != nil || r.ID == "" || r.Kind == "" || r.Tool == "" {
				res.Bad++
				continue
			}
			recs = append(recs, r)
		}
		f.Close()
		res.Files++
		states = append(states, fileState{p, st.Size(), st.ModTime()})
	}
	sort.SliceStable(recs, func(i, j int) bool {
		if recs[i].At != recs[j].At {
			return recs[i].At < recs[j].At
		}
		return recs[i].ID < recs[j].ID
	})

	if len(recs) > 0 {
		tx, err := s.db.Begin()
		if err != nil {
			return res, err
		}
		for _, r := range recs {
			applied, err := applyRecord(tx, r)
			if err != nil {
				_ = tx.Rollback()
				return res, fmt.Errorf("trace: apply %s: %w", r.ID, err)
			}
			if applied {
				res.Applied++
			} else {
				res.Duplicate++
			}
		}
		if err := tx.Commit(); err != nil {
			return res, err
		}
	}

	for _, fsx := range states {
		st, err := os.Stat(fsx.path)
		if err != nil {
			continue
		}
		quiet := grace < 0 || now().Sub(st.ModTime()) >= grace
		if st.Size() == fsx.size && quiet {
			if err := os.Remove(fsx.path); err == nil || errors.Is(err, fs.ErrNotExist) {
				res.Removed++
			}
		}
	}
	return res, nil
}

func attrsJSON(m map[string]any) sql.NullString {
	for k, v := range m {
		if v == "" || v == nil {
			delete(m, k)
		}
	}
	if len(m) == 0 {
		return sql.NullString{}
	}
	b, _ := json.Marshal(m)
	return sql.NullString{String: string(b), Valid: true}
}

// applyRecord applies one record. It returns false when the record was
// already applied (its id is already in events).
func applyRecord(tx *sql.Tx, r SpoolRecord) (bool, error) {
	traceID := r.TraceID()
	evAttrs := attrsJSON(map[string]any{"event": r.Event, "tool_name": r.ToolName, "tool_use_id": r.ToolUseID, "status": r.Status, "turn_id": r.TurnID, "instance": r.Instance})
	resEv, err := tx.Exec(`INSERT OR IGNORE INTO events(id, trace_id, kind, at, attrs) VALUES (?,?,?,?,?)`,
		r.ID, traceID, r.Kind, r.At, evAttrs)
	if err != nil {
		return false, err
	}
	if n, _ := resEv.RowsAffected(); n == 0 {
		return false, nil
	}

	var extras sql.NullString
	if r.Model != "" {
		extras = attrsJSON(map[string]any{"model": r.Model})
	}
	if _, err := tx.Exec(`INSERT INTO traces(trace_id, session_id, tool, profile, instance_id, host, started_at, extras)
		VALUES (?,?,?,?,?,?,?,?)
		ON CONFLICT(trace_id) DO UPDATE SET
			started_at  = MIN(COALESCE(traces.started_at, excluded.started_at), excluded.started_at),
			instance_id = COALESCE(traces.instance_id, excluded.instance_id),
			profile     = COALESCE(traces.profile, excluded.profile),
			host        = COALESCE(traces.host, excluded.host),
			extras      = CASE WHEN excluded.extras IS NULL THEN traces.extras
			                   ELSE json_patch(COALESCE(traces.extras,'{}'), excluded.extras) END`,
		traceID, nullStr(r.SessionID), r.Tool, nullStr(r.Profile), nullStr(r.Instance), nullStr(r.Host), r.At, extras); err != nil {
		return false, err
	}

	// Any edge after a wait means the human answered (or the wait ended).
	if r.Kind != KindWaitStart {
		if err := closeOpen(tx, traceID, SpanWait, r.At, StatusOK, "next_event"); err != nil {
			return false, err
		}
	}

	spanID := ""
	switch r.Kind {
	case KindTurnStart:
		if err := closeOpen(tx, traceID, SpanToolCall, r.At, StatusUnset, "next_turn"); err != nil {
			return false, err
		}
		if err := closeOpen(tx, traceID, SpanTurn, r.At, StatusUnset, "next_turn"); err != nil {
			return false, err
		}
		spanID = turnSpanID(traceID, r)
		if _, err := tx.Exec(`INSERT OR IGNORE INTO spans(span_id, trace_id, kind, name, started_at, status, model, attrs)
			VALUES (?,?,?,?,?,'unset',(SELECT json_extract(extras,'$.model') FROM traces WHERE trace_id = ?),?)`,
			spanID, traceID, SpanTurn, "turn", r.At, traceID, attrsJSON(map[string]any{"turn_id": r.TurnID, "summary": r.Summary})); err != nil {
			return false, err
		}

	case KindTurnEnd:
		status := r.Status
		if status == "" {
			status = StatusOK
		}
		if err := closeOpen(tx, traceID, SpanToolCall, r.At, StatusUnset, "turn_end"); err != nil {
			return false, err
		}
		spanID, err = findTurn(tx, traceID, r)
		if err != nil {
			return false, err
		}
		if spanID == "" {
			// Turn end without a seen start (codex-notify only, or the
			// start predates tracing): record a zero-length turn.
			spanID = turnSpanID(traceID, r)
			if _, err := tx.Exec(`INSERT OR IGNORE INTO spans(span_id, trace_id, kind, name, started_at, ended_at, duration_ms, status, attrs)
				VALUES (?,?,?,?,?,?,0,?,?)`, spanID, traceID, SpanTurn, "turn", r.At, r.At, status,
				attrsJSON(map[string]any{"turn_id": r.TurnID, "start": "unknown"})); err != nil {
				return false, err
			}
		} else if _, err := tx.Exec(`UPDATE spans SET ended_at = ?, duration_ms = ? - started_at, status = ?
			WHERE span_id = ? AND ended_at IS NULL`, r.At, r.At, status, spanID); err != nil {
			return false, err
		}

	case KindToolStart:
		parent, err := openTurn(tx, traceID)
		if err != nil {
			return false, err
		}
		// A still-open call with no PostToolUse (Claude's install does not
		// subscribe PostToolUse today) ends, by inference, where the next
		// one starts; a late PostToolUse overrides the inferred end.
		if err := closeOpen(tx, traceID, SpanToolCall, r.At, StatusUnset, "next_tool"); err != nil {
			return false, err
		}
		spanID = toolSpanID(traceID, r)
		res, err := tx.Exec(`UPDATE spans SET started_at = ?, duration_ms = CASE WHEN ended_at IS NULL THEN NULL ELSE ended_at - ? END,
				parent_span_id = COALESCE(parent_span_id, ?),
				attrs = json_remove(json_set(COALESCE(attrs,'{}'),'$.summary', ?), '$.start')
			WHERE span_id = ?`, r.At, r.At, nullStr(parent), r.Summary, spanID)
		if err != nil {
			return false, err
		}
		if n, _ := res.RowsAffected(); n == 0 {
			if _, err := tx.Exec(`INSERT INTO spans(span_id, trace_id, parent_span_id, kind, name, started_at, status, attrs)
				VALUES (?,?,?,?,?,?,'unset',?)`, spanID, traceID, nullStr(parent), SpanToolCall, r.ToolName, r.At,
				attrsJSON(map[string]any{"tool_use_id": r.ToolUseID, "summary": r.Summary})); err != nil {
				return false, err
			}
		}

	case KindToolEnd:
		spanID, err = findToolStart(tx, traceID, r)
		if err != nil {
			return false, err
		}
		if spanID == "" {
			// Post before Pre (async ordering) or no Pre at all: keep the
			// end; a later Pre with the same tool_use_id fills the start.
			parent, err := openTurn(tx, traceID)
			if err != nil {
				return false, err
			}
			spanID = toolSpanID(traceID, r)
			if _, err := tx.Exec(`INSERT OR IGNORE INTO spans(span_id, trace_id, parent_span_id, kind, name, started_at, ended_at, status, attrs)
				VALUES (?,?,?,?,?,?,?,?,?)`, spanID, traceID, nullStr(parent), SpanToolCall, r.ToolName, r.At, r.At, r.Status,
				attrsJSON(map[string]any{"tool_use_id": r.ToolUseID, "summary_end": r.Summary, "start": "unknown"})); err != nil {
				return false, err
			}
		} else if _, err := tx.Exec(`UPDATE spans SET ended_at = ?, duration_ms = ? - started_at, status = ?,
				attrs = json_remove(json_set(COALESCE(attrs,'{}'),'$.summary_end', ?), '$.end')
			WHERE span_id = ?`, r.At, r.At, r.Status, r.Summary, spanID); err != nil {
			return false, err
		}

	case KindWaitStart:
		parent, err := openTurn(tx, traceID)
		if err != nil {
			return false, err
		}
		spanID = "wait:" + traceID + ":" + r.ID
		if _, err := tx.Exec(`INSERT OR IGNORE INTO spans(span_id, trace_id, parent_span_id, kind, name, started_at, status, attrs)
			VALUES (?,?,?,?,?,?,'unset',?)`, spanID, traceID, nullStr(parent), SpanWait, r.Summary, r.At,
			attrsJSON(map[string]any{"tool_name": r.ToolName})); err != nil {
			return false, err
		}

	case KindSessionEnd:
		for _, k := range []string{SpanToolCall, SpanTurn} {
			if err := closeOpen(tx, traceID, k, r.At, StatusUnset, "session_end"); err != nil {
				return false, err
			}
		}
		if _, err := tx.Exec(`UPDATE traces SET ended_at = ? WHERE trace_id = ?`, r.At, traceID); err != nil {
			return false, err
		}
	}
	if spanID != "" {
		if _, err := tx.Exec(`UPDATE events SET span_id = ? WHERE id = ?`, spanID, r.ID); err != nil {
			return false, err
		}
	}
	return true, nil
}

func nullStr(s string) sql.NullString { return sql.NullString{String: s, Valid: s != ""} }

func turnSpanID(traceID string, r SpoolRecord) string {
	if r.TurnID != "" {
		return "turn:" + traceID + ":" + r.TurnID
	}
	return "turn:" + traceID + ":" + r.ID
}

func toolSpanID(traceID string, r SpoolRecord) string {
	if r.ToolUseID != "" {
		return "tool:" + traceID + ":" + r.ToolUseID
	}
	return "tool:" + traceID + ":" + r.ID
}

func openTurn(tx *sql.Tx, traceID string) (string, error) {
	var id string
	err := tx.QueryRow(`SELECT span_id FROM spans WHERE trace_id = ? AND kind = 'turn' AND ended_at IS NULL
		ORDER BY started_at DESC LIMIT 1`, traceID).Scan(&id)
	if errors.Is(err, sql.ErrNoRows) {
		return "", nil
	}
	return id, err
}

func findTurn(tx *sql.Tx, traceID string, r SpoolRecord) (string, error) {
	if r.TurnID != "" {
		var id string
		err := tx.QueryRow(`SELECT span_id FROM spans WHERE span_id = ?`, turnSpanID(traceID, r)).Scan(&id)
		if err == nil {
			return id, nil
		}
		if !errors.Is(err, sql.ErrNoRows) {
			return "", err
		}
	}
	return openTurn(tx, traceID)
}

// findToolStart pairs a tool end with its start: by tool_use_id first, then
// (no id) the oldest still-open or end-inferred call of the same tool name.
func findToolStart(tx *sql.Tx, traceID string, r SpoolRecord) (string, error) {
	var id string
	if r.ToolUseID != "" {
		err := tx.QueryRow(`SELECT span_id FROM spans WHERE span_id = ?`, toolSpanID(traceID, r)).Scan(&id)
		if errors.Is(err, sql.ErrNoRows) {
			return "", nil
		}
		return id, err
	}
	err := tx.QueryRow(`SELECT span_id FROM spans WHERE trace_id = ? AND kind = 'tool_call' AND name = ?
		AND (ended_at IS NULL OR json_extract(attrs,'$.end') = 'inferred')
		ORDER BY started_at ASC, rowid ASC LIMIT 1`, traceID, r.ToolName).Scan(&id)
	if errors.Is(err, sql.ErrNoRows) {
		return "", nil
	}
	return id, err
}

// closeOpen ends every open span of kind in the trace at at. Tool calls
// closed this way (no PostToolUse seen) are marked end=inferred so a late
// PostToolUse still overrides them.
func closeOpen(tx *sql.Tx, traceID, kind string, at int64, status, reason string) error {
	_, err := tx.Exec(`UPDATE spans SET ended_at = ?, duration_ms = MAX(0, ? - started_at), status = ?,
			attrs = json_set(COALESCE(attrs,'{}'), '$.end', 'inferred', '$.closed_by', ?)
		WHERE trace_id = ? AND kind = ? AND ended_at IS NULL`, at, at, status, reason, traceID, kind)
	return err
}
