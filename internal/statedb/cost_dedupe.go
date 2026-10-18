package statedb

import (
	"database/sql"
	"log/slog"
	"time"

	"github.com/asheshgoplani/agent-deck/internal/logging"
)

// costHookDedupeKey marks foldLegacyHookCostRows as done in metadata.
const costHookDedupeKey = "cost_events_hook_dedupe_v1"

// Window in which a Stop hook row and an importer row record the same turn:
// the hook is written when the turn stops, at or shortly after the final
// assistant record the importer reads.
const (
	hookDedupeBefore = 2 * time.Minute
	hookDedupeAfter  = 10 * time.Minute
)

// foldLegacyHookCostRows removes, once, the Stop hook rows that earlier
// versions stored next to the recall importer's row for the same turn. The
// hook keyed its row "<instance>_<unix nanos>" and the importer
// "<instance>_<record uuid>", so a turn seen by both counted twice. A hook row
// is dropped only when it pairs with an importer row of the same session,
// model and all four token counts, timestamped within the window; each
// importer row absorbs at most one hook row (the closest in time). Anything
// without such a match is kept. It runs inside Migrate and records itself in
// metadata, so it is idempotent.
func foldLegacyHookCostRows(tx *sql.Tx) error {
	var done string
	if err := tx.QueryRow(`SELECT value FROM metadata WHERE key = ?`, costHookDedupeKey).Scan(&done); err == nil && done != "" {
		return nil
	}
	type row struct {
		id, session, model string
		in, out, cr, cw    int64
		ts                 time.Time
	}
	scan := func(rows *sql.Rows) ([]row, error) {
		defer func() { _ = rows.Close() }()
		var out []row
		for rows.Next() {
			var r row
			var ts string
			if err := rows.Scan(&r.id, &r.session, &r.model, &r.in, &r.out, &r.cr, &r.cw, &ts); err != nil {
				return nil, err
			}
			parsed, err := time.Parse(time.RFC3339, ts)
			if err != nil {
				continue // an unreadable timestamp is never a clear match
			}
			r.ts = parsed
			out = append(out, r)
		}
		return out, rows.Err()
	}
	const cols = `id, session_id, model, input_tokens, output_tokens, cache_read_tokens, cache_write_tokens, timestamp`
	// "<session>_<digits>": the hook's id. Anything else after the session
	// id (a uuid, a message id) is an importer row.
	const hookID = `substr(id, 1, length(session_id) + 1) = session_id || '_'
		AND length(id) > length(session_id) + 1
		AND substr(id, length(session_id) + 2) NOT GLOB '*[^0-9]*'`
	hookRows, err := tx.Query(`SELECT ` + cols + ` FROM cost_events WHERE ` + hookID + ` ORDER BY timestamp`)
	if err != nil {
		return err
	}
	hooks, err := scan(hookRows)
	if err != nil {
		return err
	}
	used := map[string]bool{}
	folded := 0
	for _, h := range hooks {
		candRows, err := tx.Query(`SELECT `+cols+` FROM cost_events
			WHERE session_id = ? AND model = ? AND input_tokens = ? AND output_tokens = ?
			AND cache_read_tokens = ? AND cache_write_tokens = ? AND NOT (`+hookID+`)`,
			h.session, h.model, h.in, h.out, h.cr, h.cw)
		if err != nil {
			return err
		}
		cands, err := scan(candRows)
		if err != nil {
			return err
		}
		best, bestGap := "", time.Duration(-1)
		for _, c := range cands {
			gap := h.ts.Sub(c.ts)
			if used[c.id] || gap < -hookDedupeBefore || gap > hookDedupeAfter {
				continue
			}
			if gap < 0 {
				gap = -gap
			}
			if bestGap < 0 || gap < bestGap {
				best, bestGap = c.id, gap
			}
		}
		if best == "" {
			continue
		}
		used[best] = true
		if _, err := tx.Exec(`DELETE FROM cost_events WHERE id = ?`, h.id); err != nil {
			return err
		}
		folded++
	}
	// Only when something was removed, and only to the agent-deck log: a
	// CLI command's stdout/stderr may be JSON its caller parses.
	if folded > 0 {
		logging.ForComponent(logging.CompStorage).Info("statedb: folded legacy hook cost rows", slog.Int("folded", folded), slog.Int("hook_rows", len(hooks)))
	}
	_, err = tx.Exec(`INSERT INTO metadata (key, value) VALUES (?, ?)
		ON CONFLICT(key) DO UPDATE SET value = excluded.value`, costHookDedupeKey, time.Now().UTC().Format(time.RFC3339))
	return err
}
