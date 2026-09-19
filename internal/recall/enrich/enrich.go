// Package enrich drains enrich_queue and writes artifact rows: the cheap
// (T1) classifiers of the design run here, on the SQL rows the ingest
// already wrote (session counters, tool_call, msg classes, the card's
// hints) and never by re-parsing a transcript. Every artifact carries
// input_rev, the session's derived_rev at production time; a session whose
// content moved since (input_rev < derived_rev) shows the artifact as
// stale in every tier until the next drain rewrites it.
//
// There are no worker goroutines: a drain is a bounded pass (row limit,
// wall budget) that runs in the caller's goroutine, under the same load
// gate as the sweep, and ends with the command that asked for it.
package enrich

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/asheshgoplani/agent-deck/internal/recall/classify"
	"github.com/asheshgoplani/agent-deck/internal/recall/reader"
	"github.com/asheshgoplani/agent-deck/internal/recall/store"
)

// Cost classes (enrich_queue.cost_class).
const (
	CostFree  = "free"
	CostCheap = "cheap"
	CostLLM   = "llm"
)

// Artifact kinds this package produces.
const (
	KindLostTime    = "lost_time"
	KindSessionKind = "session_kind"
	KindOutcome     = "outcome"
)

// CheapKinds are the classifiers drained by cost class "cheap", in the
// order they run.
var CheapKinds = []string{KindLostTime, KindSessionKind, KindOutcome}

// Producer names the rules classifier on every artifact it writes.
const Producer = "rules"

// Queue states.
const (
	StatePending = "pending"
	StateDone    = "done"
	StateFailed  = "failed"
)

// maxAttempts is when a failing row stops being retried.
const maxAttempts = 5

// Gate is ingest.Gate's contract (nil: no gate).
type Gate interface{ Check() error }

// Options configures one drain.
type Options struct {
	// CostClass selects the queue rows (default CostCheap). CostLLM rows
	// are never drained here: the design keeps LLM enrichment manual.
	CostClass string
	// Kinds restricts the drain to these kinds (nil: every kind of the
	// cost class).
	Kinds []string
	// Limit caps the rows one drain processes (0: every pending row; the
	// wall budget bounds the pass instead).
	Limit int
	// Budget bounds the drain in wall time (nil: unlimited).
	Budget *reader.Budget
	Gate   Gate
	Now    func() time.Time
}

// Result summarises one drain.
type Result struct {
	Pending   int   `json:"pending"`
	Processed int   `json:"processed"`
	Written   int   `json:"written"`
	Failed    int   `json:"failed"`
	Deferred  int   `json:"deferred"`
	ElapsedMS int64 `json:"elapsed_ms"`
}

// Drainer runs drains against one open store.
type Drainer struct {
	st   *store.Store
	opts Options
}

// New returns a Drainer over st.
func New(st *store.Store, opts Options) *Drainer {
	if opts.CostClass == "" {
		opts.CostClass = CostCheap
	}
	if opts.Now == nil {
		opts.Now = time.Now
	}
	return &Drainer{st: st, opts: opts}
}

// Execer is what Enqueue needs: a *sql.Tx or *sql.DB.
type Execer interface {
	Exec(query string, args ...any) (sql.Result, error)
}

// Enqueue marks every cheap kind of sessID pending. Called after every
// content change (the ingest bumps derived_rev) and after every card
// re-projection (hints changed), so a stale artifact is always queued for
// the next drain. A done row goes back to pending; a failed row keeps its
// attempt count so a classifier bug cannot spin.
func Enqueue(db Execer, sessID int64) error {
	for _, kind := range CheapKinds {
		if _, err := db.Exec(`INSERT INTO enrich_queue(sess_id, kind, cost_class, state) VALUES (?, ?, ?, ?)
			ON CONFLICT(sess_id, kind) DO UPDATE SET state=?, not_before=0 WHERE state<>?`,
			sessID, kind, CostCheap, StatePending, StatePending, StateFailed); err != nil {
			return fmt.Errorf("recall: enqueue %s: %w", kind, err)
		}
	}
	return nil
}

// ErrLLMNotAutomatic is returned for a drain of the llm cost class.
var ErrLLMNotAutomatic = errors.New("recall: llm enrichment is never drained automatically (docs/recall.md, phase 4)")

// Drain processes pending rows of the cost class, one session and kind at
// a time, until the limit, the budget or the queue runs out.
func (d *Drainer) Drain(ctx context.Context) (res Result, err error) {
	start := d.opts.Now()
	if d.opts.CostClass == CostLLM {
		return res, ErrLLMNotAutomatic
	}
	if d.opts.Gate != nil {
		if err := d.opts.Gate.Check(); err != nil {
			return res, err
		}
	}
	rows, total, err := d.pending()
	if err != nil {
		return res, err
	}
	res.Pending = total
	// Deferred is every pending row this pass did not reach: beyond the
	// limit, past the budget, or after a cancel.
	defer func() {
		res.Deferred = total - res.Processed
		res.ElapsedMS = d.opts.Now().Sub(start).Milliseconds()
	}()
	for _, q := range rows {
		if err := ctx.Err(); err != nil {
			return res, err
		}
		if d.opts.Budget.Expired() {
			break
		}
		res.Processed++
		if err := d.process(q); err != nil {
			res.Failed++
			if ferr := d.fail(q, err); ferr != nil {
				return res, ferr
			}
			continue
		}
		res.Written++
	}
	return res, nil
}

type queued struct {
	sessID   int64
	kind     string
	attempts int
}

// pending returns the rows this pass may process (newest sessions first,
// at most Limit) and the total number pending.
func (d *Drainer) pending() ([]queued, int, error) {
	where := ` FROM enrich_queue WHERE cost_class=? AND state=? AND not_before<=?`
	args := []any{d.opts.CostClass, StatePending, d.opts.Now().Unix()}
	if len(d.opts.Kinds) > 0 {
		where += ` AND kind IN (?` + strings.Repeat(",?", len(d.opts.Kinds)-1) + `)`
		for _, k := range d.opts.Kinds {
			args = append(args, k)
		}
	}
	var total int
	if err := d.st.W.QueryRow(`SELECT count(*)`+where, args...).Scan(&total); err != nil {
		return nil, 0, err
	}
	q := `SELECT sess_id, kind, attempts` + where + ` ORDER BY priority, sess_id DESC, kind`
	if d.opts.Limit > 0 {
		q += ` LIMIT ?`
		args = append(args, d.opts.Limit)
	}
	rows, err := d.st.W.Query(q, args...)
	if err != nil {
		return nil, 0, err
	}
	defer rows.Close()
	var out []queued
	for rows.Next() {
		var r queued
		if err := rows.Scan(&r.sessID, &r.kind, &r.attempts); err != nil {
			return nil, 0, err
		}
		out = append(out, r)
	}
	return out, total, rows.Err()
}

// process classifies one (session, kind) inside one transaction: the
// input rows and derived_rev are read and the artifact written under the
// same snapshot, so input_rev is exactly the revision the artifact saw.
func (d *Drainer) process(q queued) error {
	tx, err := d.st.W.Begin()
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	in, err := loadInput(tx, q.sessID)
	if errors.Is(err, sql.ErrNoRows) {
		// The session is gone (a rebuild in between): drop the row.
		if _, err := tx.Exec(`DELETE FROM enrich_queue WHERE sess_id=? AND kind=?`, q.sessID, q.kind); err != nil {
			return err
		}
		return tx.Commit()
	}
	if err != nil {
		return err
	}
	art, err := Classify(q.kind, in)
	if err != nil {
		return err
	}
	if _, err := tx.Exec(`INSERT INTO artifact(sess_id, kind, body, body_json, producer, producer_ver, confidence, input_rev, created_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(sess_id, kind, producer, producer_ver) DO UPDATE SET body=excluded.body, body_json=excluded.body_json,
		confidence=excluded.confidence, input_rev=excluded.input_rev, created_at=excluded.created_at`,
		q.sessID, q.kind, art.Body, art.BodyJSON, Producer, strconv.Itoa(classify.Current().Version), art.Confidence, in.DerivedRev, d.opts.Now().Unix()); err != nil {
		return fmt.Errorf("recall: write artifact: %w", err)
	}
	if _, err := tx.Exec(`UPDATE enrich_queue SET state=?, attempts=?, last_error='' WHERE sess_id=? AND kind=?`,
		StateDone, q.attempts+1, q.sessID, q.kind); err != nil {
		return err
	}
	return tx.Commit()
}

// fail records a classifier error with a backoff; after maxAttempts the
// row is parked as failed and `recall enrich --retry-failed` is the way back.
func (d *Drainer) fail(q queued, cause error) error {
	attempts := q.attempts + 1
	state := StatePending
	if attempts >= maxAttempts {
		state = StateFailed
	}
	backoff := time.Duration(attempts*attempts) * time.Minute
	_, err := d.st.W.Exec(`UPDATE enrich_queue SET state=?, attempts=?, not_before=?, last_error=? WHERE sess_id=? AND kind=?`,
		state, attempts, d.opts.Now().Add(backoff).Unix(), clip(cause.Error(), 500), q.sessID, q.kind)
	return err
}

// RetryFailed puts every failed row of the cost class back to pending.
func (d *Drainer) RetryFailed() (int, error) {
	res, err := d.st.W.Exec(`UPDATE enrich_queue SET state=?, attempts=0, not_before=0 WHERE cost_class=? AND state=?`, StatePending, d.opts.CostClass, StateFailed)
	if err != nil {
		return 0, err
	}
	n, _ := res.RowsAffected()
	return int(n), nil
}

// QueueCounts is enrich_queue by cost class and state, for `recall status`.
func QueueCounts(db *sql.DB) (map[string]int, error) {
	rows, err := db.Query(`SELECT cost_class || '/' || state, count(*) FROM enrich_queue GROUP BY 1`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]int{}
	for rows.Next() {
		var k string
		var n int
		if err := rows.Scan(&k, &n); err != nil {
			return nil, err
		}
		out[k] = n
	}
	return out, rows.Err()
}

// ---- inputs ----------------------------------------------------------------

// Input is everything a classifier may look at: SQL rows only.
type Input struct {
	SessID     int64
	DerivedRev int64
	Turns      int
	ToolCalls  int
	Errors     int
	Interrupts int
	Compacts   int
	Sidechain  bool
	Harness    string
	// Hints is the card's projected "k=v k=v" string; Hint looks one up.
	Hints string
	// Classes counts msg rows per class name.
	Classes map[string]int
	// Calls are the tool_call rows in time order.
	Calls []Call
}

// Call is one tool_call row.
type Call struct {
	Name       string
	IsError    bool
	DurationMS int64
}

// Hint returns the value of key in the projected hints, or "".
func (in *Input) Hint(key string) string {
	for _, kv := range strings.Fields(in.Hints) {
		if k, v, ok := strings.Cut(kv, "="); ok && k == key {
			return v
		}
	}
	return ""
}

type querier interface {
	QueryRow(query string, args ...any) *sql.Row
	Query(query string, args ...any) (*sql.Rows, error)
}

func loadInput(db querier, sessID int64) (*Input, error) {
	in := &Input{SessID: sessID, Classes: map[string]int{}}
	var sidechain int
	if err := db.QueryRow(`SELECT s.derived_rev, s.turns, s.tool_calls, s.errors, s.interrupts, s.compacts, s.is_sidechain, s.harness, COALESCE(c.hints,'')
		FROM session s LEFT JOIN card c ON c.sess_id=s.sess_id WHERE s.sess_id=?`, sessID).Scan(
		&in.DerivedRev, &in.Turns, &in.ToolCalls, &in.Errors, &in.Interrupts, &in.Compacts, &sidechain, &in.Harness, &in.Hints); err != nil {
		return nil, err
	}
	in.Sidechain = sidechain == 1
	rows, err := db.Query(`SELECT class, count(*) FROM msg WHERE sess_id=? GROUP BY class`, sessID)
	if err != nil {
		return nil, err
	}
	for rows.Next() {
		var class, n int
		if err := rows.Scan(&class, &n); err != nil {
			rows.Close()
			return nil, err
		}
		in.Classes[classify.Class(class).String()] = n
	}
	rows.Close()
	rows, err = db.Query(`SELECT name, is_error, duration_ms FROM tool_call WHERE sess_id=? AND name<>'' ORDER BY ts, call_id`, sessID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var c Call
		var isErr int
		if err := rows.Scan(&c.Name, &isErr, &c.DurationMS); err != nil {
			return nil, err
		}
		c.IsError = isErr == 1
		in.Calls = append(in.Calls, c)
	}
	return in, rows.Err()
}

// ---- classifiers -----------------------------------------------------------

// Artifact is what a classifier returns.
type Artifact struct {
	Body       string
	BodyJSON   string
	Confidence float64
}

// Classify runs the classifier of kind over in.
func Classify(kind string, in *Input) (Artifact, error) {
	rules := classify.Current()
	switch kind {
	case KindLostTime:
		return lostTime(in, rules.LostTime), nil
	case KindSessionKind:
		return sessionKind(in, rules.SessionKind), nil
	case KindOutcome:
		return outcome(in, rules.Outcome), nil
	}
	return Artifact{}, fmt.Errorf("recall: unknown artifact kind %q", kind)
}

type toolLoss struct {
	Name       string  `json:"name"`
	Calls      int     `json:"calls"`
	Errors     int     `json:"errors"`
	ErrorShare float64 `json:"error_share"`
	RetryLoops int     `json:"retry_loops"`
	ErrorMS    int64   `json:"error_ms"`
}

type lostTimeJSON struct {
	Tools       []toolLoss `json:"tools,omitempty"`
	SlowCalls   int        `json:"slow_calls"`
	SlowestName string     `json:"slowest_tool,omitempty"`
	SlowestMS   int64      `json:"slowest_ms,omitempty"`
	Interrupts  int        `json:"interrupts"`
	Compacts    int        `json:"compacts"`
	ErrorMS     int64      `json:"error_ms"`
}

// lostTime answers "where did we lose time": tools that errored often or
// looped on retries, calls over the slow threshold, interrupts and
// compactions, all from tool_call and the session counters.
func lostTime(in *Input, r classify.LostTimeRules) Artifact {
	per := map[string]*toolLoss{}
	var order []string
	out := lostTimeJSON{Interrupts: in.Interrupts, Compacts: in.Compacts}
	run, runName := 0, ""
	for _, c := range in.Calls {
		t := per[c.Name]
		if t == nil {
			t = &toolLoss{Name: c.Name}
			per[c.Name] = t
			order = append(order, c.Name)
		}
		t.Calls++
		if c.IsError {
			t.Errors++
			t.ErrorMS += c.DurationMS
			out.ErrorMS += c.DurationMS
			if c.Name == runName {
				run++
			} else {
				run, runName = 1, c.Name
			}
			if r.RetryWindow > 0 && run == r.RetryWindow {
				t.RetryLoops++
			}
		} else {
			run, runName = 0, ""
		}
		if r.SlowCallMS > 0 && c.DurationMS >= r.SlowCallMS {
			out.SlowCalls++
		}
		if c.DurationMS > out.SlowestMS {
			out.SlowestMS, out.SlowestName = c.DurationMS, c.Name
		}
	}
	var named []toolLoss
	for _, name := range order {
		t := per[name]
		t.ErrorShare = float64(t.Errors) / float64(t.Calls)
		if (t.Errors >= r.MinErrors && t.ErrorShare >= r.ErrorShareMin) || t.RetryLoops > 0 {
			named = append(named, *t)
		}
	}
	sort.SliceStable(named, func(i, j int) bool { return named[i].Errors > named[j].Errors })
	if r.TopTools > 0 && len(named) > r.TopTools {
		named = named[:r.TopTools]
	}
	out.Tools = named
	var parts []string
	for _, t := range named {
		s := fmt.Sprintf("%s: %d of %d calls errored", t.Name, t.Errors, t.Calls)
		if t.RetryLoops > 0 {
			s += fmt.Sprintf(", %d retry loop(s)", t.RetryLoops)
		}
		parts = append(parts, s)
	}
	if out.SlowCalls > 0 {
		parts = append(parts, fmt.Sprintf("%d call(s) over %s (slowest %s %s)", out.SlowCalls, ms(r.SlowCallMS), out.SlowestName, ms(out.SlowestMS)))
	}
	if in.Interrupts > 0 {
		parts = append(parts, fmt.Sprintf("%d interrupt(s)", in.Interrupts))
	}
	if in.Compacts > 0 {
		parts = append(parts, fmt.Sprintf("%d compaction(s)", in.Compacts))
	}
	body := "no time lost to tool errors, retries or interrupts"
	if len(parts) > 0 {
		body = strings.Join(parts, "; ")
	}
	return Artifact{Body: body, BodyJSON: mustJSON(out), Confidence: 1}
}

// Session kinds.
const (
	KindValueConductor   = "conductor"
	KindValueSubagent    = "subagent"
	KindValueWorker      = "worker"
	KindValueInteractive = "interactive"
)

type sessionKindJSON struct {
	Kind          string  `json:"kind"`
	Reason        string  `json:"reason"`
	Heartbeats    int     `json:"heartbeats"`
	HeartbeatRate float64 `json:"heartbeat_share"`
}

// sessionKind decides conductor / subagent / worker / interactive from the
// hints the human or the launcher wrote, the sidechain flag and the
// heartbeat share of user messages.
func sessionKind(in *Input, r classify.SessionKindRules) Artifact {
	hb := in.Classes[classify.Heartbeat.String()]
	users := hb + in.Classes[classify.Prompt.String()] + in.Classes[classify.SkillLoad.String()] + in.Classes[classify.Meta.String()]
	share := 0.0
	if users > 0 {
		share = float64(hb) / float64(users)
	}
	out := sessionKindJSON{Heartbeats: hb, HeartbeatRate: share}
	conf := 1.0
	switch {
	case in.Sidechain:
		out.Kind, out.Reason = KindValueSubagent, "subagent transcript"
	case r.ConductorPurposePrefix != "" && strings.HasPrefix(in.Hint("purpose"), r.ConductorPurposePrefix):
		out.Kind, out.Reason = KindValueConductor, "purpose hint"
	case hb >= r.MinHeartbeats && share >= r.HeartbeatShareMin:
		out.Kind, out.Reason = KindValueConductor, fmt.Sprintf("%d heartbeats, %.0f%% of user messages", hb, 100*share)
		conf = 0.7
	case r.ParentHintKey != "" && in.Hint(r.ParentHintKey) != "":
		out.Kind, out.Reason = KindValueWorker, r.ParentHintKey+" hint"
	default:
		out.Kind, out.Reason = KindValueInteractive, "no conductor or parent signal"
		conf = 0.6
	}
	return Artifact{Body: out.Kind + " (" + out.Reason + ")", BodyJSON: mustJSON(out), Confidence: conf}
}

// Outcome values.
const (
	OutcomeWorked    = "worked"
	OutcomeFailed    = "failed"
	OutcomeAbandoned = "abandoned"
	OutcomeUnknown   = "unknown"
)

type outcomeJSON struct {
	Outcome    string  `json:"outcome"`
	Source     string  `json:"source"`
	ErrorShare float64 `json:"error_share"`
	Interrupts int     `json:"interrupts"`
}

// outcome is the annotated outcome when there is one (certain), else a
// structural guess from the error share and interrupt count, labelled as
// a guess by its confidence.
func outcome(in *Input, r classify.OutcomeRules) Artifact {
	share := 0.0
	if in.ToolCalls > 0 {
		share = float64(in.Errors) / float64(in.ToolCalls)
	}
	out := outcomeJSON{ErrorShare: share, Interrupts: in.Interrupts}
	if v := in.Hint(r.HintKey); v != "" {
		out.Outcome, out.Source = v, "hint"
		return Artifact{Body: v + " (annotated)", BodyJSON: mustJSON(out), Confidence: 1}
	}
	switch {
	case r.InterruptsAbandoned > 0 && in.Interrupts >= r.InterruptsAbandoned:
		out.Outcome, out.Source = OutcomeAbandoned, fmt.Sprintf("%d interrupts", in.Interrupts)
	case in.ToolCalls >= r.MinToolCalls && share >= r.ErrorShareFailed:
		out.Outcome, out.Source = OutcomeFailed, fmt.Sprintf("%.0f%% of %d tool calls errored", 100*share, in.ToolCalls)
	default:
		out.Outcome, out.Source = OutcomeUnknown, "not annotated"
		return Artifact{Body: OutcomeUnknown + " (not annotated: session annotate --outcome)", BodyJSON: mustJSON(out), Confidence: 0.3}
	}
	return Artifact{Body: out.Outcome + "? (" + out.Source + ")", BodyJSON: mustJSON(out), Confidence: 0.5}
}

func ms(n int64) string {
	return (time.Duration(n) * time.Millisecond).Round(time.Second).String()
}

func mustJSON(v any) string {
	b, err := json.Marshal(v)
	if err != nil {
		return "{}"
	}
	return string(b)
}

func clip(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n]
}
