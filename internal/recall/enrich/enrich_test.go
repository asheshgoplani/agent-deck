package enrich

import (
	"context"
	"encoding/json"
	"errors"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/asheshgoplani/agent-deck/internal/recall/classify"
	"github.com/asheshgoplani/agent-deck/internal/recall/store"
)

func openStore(t *testing.T) *store.Store {
	t.Helper()
	st, err := store.Open(filepath.Join(t.TempDir(), "recall.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(st.Close)
	return st
}

// seedSession writes one session with tool calls and message classes by
// hand: the classifiers see SQL rows only, so no transcript is needed.
func seedSession(t *testing.T, st *store.Store, native, hints string, sidechain bool, calls []Call, classes map[classify.Class]int) int64 {
	t.Helper()
	errs := 0
	for _, c := range calls {
		if c.IsError {
			errs++
		}
	}
	interrupts := classes[classify.Interrupt]
	res, err := st.W.Exec(`INSERT INTO session(host_uid, harness, profile, native_id, tool_calls, errors, interrupts, is_sidechain, derived_rev) VALUES ('local','claude','p',?,?,?,?,?,3)`,
		native, len(calls), errs, interrupts, boolInt(sidechain))
	if err != nil {
		t.Fatal(err)
	}
	id, _ := res.LastInsertId()
	if _, err := st.W.Exec(`INSERT INTO card(sess_id, title, hints, tags, summary, preview) VALUES (?, '', ?, '', '', '')`, id, hints); err != nil {
		t.Fatal(err)
	}
	seq := 0
	for class, n := range classes {
		for i := 0; i < n; i++ {
			seq++
			if _, err := st.W.Exec(`INSERT INTO msg(sess_id, src_id, seq, role, class, ts, rec_off, rec_len) VALUES (?, 1, ?, 1, ?, 0, 0, 0)`, id, seq, int(class)); err != nil {
				t.Fatal(err)
			}
		}
	}
	for i, c := range calls {
		if _, err := st.W.Exec(`INSERT INTO tool_call(sess_id, src_id, name, ts, duration_ms, is_error) VALUES (?, 1, ?, ?, ?, ?)`, id, c.Name, i, c.DurationMS, boolInt(c.IsError)); err != nil {
			t.Fatal(err)
		}
	}
	return id
}

func boolInt(b bool) int {
	if b {
		return 1
	}
	return 0
}

func TestDrain_WritesArtifactsFromRowsAndMarksInputRev(t *testing.T) {
	st := openStore(t)
	calls := []Call{{"Bash", true, 100}, {"Bash", true, 100}, {"Bash", true, 100}, {"Bash", false, 100}, {"Read", false, 5}, {"Edit", false, 90000}}
	id := seedSession(t, st, "s1", "purpose=conductor ops ticket=SB-1", false, calls, map[classify.Class]int{classify.Prompt: 2, classify.Heartbeat: 6, classify.Assist: 8, classify.Interrupt: 1})
	if err := Enqueue(st.W, id); err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 9, 19, 12, 0, 0, 0, time.UTC)
	d := New(st, Options{Now: func() time.Time { return now }})
	res, err := d.Drain(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if res.Pending != 3 || res.Written != 3 || res.Failed != 0 || res.Deferred != 0 {
		t.Fatalf("drain: %+v", res)
	}
	rows, err := st.R.Query(`SELECT kind, body, body_json, producer, confidence, input_rev FROM artifact WHERE sess_id=? ORDER BY kind`, id)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	got := map[string]string{}
	for rows.Next() {
		var kind, body, bodyJSON, producer string
		var conf float64
		var rev int64
		if err := rows.Scan(&kind, &body, &bodyJSON, &producer, &conf, &rev); err != nil {
			t.Fatal(err)
		}
		if producer != Producer || rev != 3 || !json.Valid([]byte(bodyJSON)) {
			t.Fatalf("%s: producer %q input_rev %d json %q", kind, producer, rev, bodyJSON)
		}
		got[kind] = body
	}
	if !strings.Contains(got[KindLostTime], "Bash: 3 of 4 calls errored, 1 retry loop(s)") || !strings.Contains(got[KindLostTime], "1 call(s) over 1m0s (slowest Edit 1m30s)") || !strings.Contains(got[KindLostTime], "1 interrupt(s)") {
		t.Fatalf("lost_time = %q", got[KindLostTime])
	}
	if got[KindSessionKind] != "conductor (purpose hint)" {
		t.Fatalf("session_kind = %q", got[KindSessionKind])
	}
	if !strings.HasPrefix(got[KindOutcome], "failed? (50% of 6 tool calls errored)") {
		t.Fatalf("outcome = %q", got[KindOutcome])
	}
	// Nothing pending afterwards; a second drain is a no-op.
	res, err = d.Drain(context.Background())
	if err != nil || res.Pending != 0 {
		t.Fatalf("second drain: %+v %v", res, err)
	}
	// A hint change re-queues and the annotated outcome wins outright.
	if _, err := st.W.Exec(`UPDATE card SET hints='outcome=worked' WHERE sess_id=?`, id); err != nil {
		t.Fatal(err)
	}
	if err := Enqueue(st.W, id); err != nil {
		t.Fatal(err)
	}
	if res, err = d.Drain(context.Background()); err != nil || res.Written != 3 {
		t.Fatalf("redrain: %+v %v", res, err)
	}
	var body string
	var conf float64
	if err := st.R.QueryRow(`SELECT body, confidence FROM artifact WHERE sess_id=? AND kind=?`, id, KindOutcome).Scan(&body, &conf); err != nil {
		t.Fatal(err)
	}
	if body != "worked (annotated)" || conf != 1 {
		t.Fatalf("annotated outcome: %q %v", body, conf)
	}
}

func TestClassifiers_TableOverSyntheticInput(t *testing.T) {
	r := classify.Current()
	cases := []struct {
		name string
		in   Input
		kind string
		want string
		conf float64
	}{
		{"quiet", Input{Calls: []Call{{"Read", false, 10}}}, KindLostTime, "no time lost to tool errors, retries or interrupts", 1},
		{"subagent", Input{Sidechain: true, Hints: "purpose=conductor x"}, KindSessionKind, "subagent (subagent transcript)", 1},
		{"heartbeats", Input{Classes: map[string]int{"heartbeat": 5, "prompt": 2}}, KindSessionKind, "conductor (5 heartbeats, 71% of user messages)", 0.7},
		{"worker", Input{Hints: "parent=abc", Classes: map[string]int{}}, KindSessionKind, "worker (parent hint)", 1},
		{"interactive", Input{Classes: map[string]int{"prompt": 3}}, KindSessionKind, "interactive (no conductor or parent signal)", 0.6},
		{"abandoned", Input{Interrupts: 3, Classes: map[string]int{}}, KindOutcome, "abandoned? (3 interrupts)", 0.5},
		{"unknown", Input{ToolCalls: 2, Errors: 2}, KindOutcome, "unknown (not annotated: session annotate --outcome)", 0.3},
	}
	for _, c := range cases {
		got, err := Classify(c.kind, &c.in)
		if err != nil {
			t.Fatal(err)
		}
		if got.Body != c.want || got.Confidence != c.conf {
			t.Errorf("%s: %q (%.1f) want %q (%.1f)", c.name, got.Body, got.Confidence, c.want, c.conf)
		}
	}
	if _, err := Classify("nope", &Input{}); err == nil {
		t.Fatal("unknown kind must error")
	}
	if r.LostTime.RetryWindow < 2 {
		t.Fatalf("rules.json retry_window = %d", r.LostTime.RetryWindow)
	}
}

func TestDrain_GateBudgetLimitAndLLMRefused(t *testing.T) {
	st := openStore(t)
	for i := 0; i < 4; i++ {
		id := seedSession(t, st, "s"+string(rune('a'+i)), "", false, nil, map[classify.Class]int{classify.Prompt: 1})
		if err := Enqueue(st.W, id); err != nil {
			t.Fatal(err)
		}
	}
	gated := errors.New("busy")
	if _, err := New(st, Options{Gate: gateFunc(func() error { return gated })}).Drain(context.Background()); !errors.Is(err, gated) {
		t.Fatalf("gate: %v", err)
	}
	if _, err := New(st, Options{CostClass: CostLLM}).Drain(context.Background()); !errors.Is(err, ErrLLMNotAutomatic) {
		t.Fatalf("llm: %v", err)
	}
	res, err := New(st, Options{Limit: 5}).Drain(context.Background())
	if err != nil || res.Pending != 12 || res.Written != 5 || res.Deferred != 7 {
		t.Fatalf("limit: %+v %v", res, err)
	}
	var left int
	_ = st.R.QueryRow(`SELECT count(*) FROM enrich_queue WHERE state='pending'`).Scan(&left)
	if left != 7 {
		t.Fatalf("pending after a limited drain = %d want 7", left)
	}
	var cnt map[string]int
	if cnt, err = QueueCounts(st.R); err != nil || cnt["cheap/pending"] != 7 || cnt["cheap/done"] != 5 {
		t.Fatalf("counts: %v %v", cnt, err)
	}
}

type gateFunc func() error

func (g gateFunc) Check() error { return g() }

// TestDrain_RequeuesStaleAndUnclassifiedSessions is the defect the round-2
// review found: an artifact with input_rev < derived_rev and a done queue
// row (a pass cut between the derived_rev bump and the card projection)
// was never drained again, so `recall enrich` answered "0 pending" while
// every tier printed the stale marker telling the user to run it. A drain
// now queues such sessions itself, and a session with no artifact at all
// (indexed before the classifiers existed), never one that is a card
// pulled from another machine.
func TestDrain_RequeuesStaleAndUnclassifiedSessions(t *testing.T) {
	st := openStore(t)
	stale := seedSession(t, st, "stale", "", false, nil, map[classify.Class]int{classify.Prompt: 1})
	if err := Enqueue(st.W, stale); err != nil {
		t.Fatal(err)
	}
	d := New(st, Options{})
	if res, err := d.Drain(context.Background()); err != nil || res.Written != 3 {
		t.Fatalf("first drain: %+v %v", res, err)
	}
	// The content moves without anything queueing the session: exactly
	// the state a cancel between the two ingest transactions used to leave.
	if _, err := st.W.Exec(`UPDATE session SET derived_rev=derived_rev+1, interrupts=3 WHERE sess_id=?`, stale); err != nil {
		t.Fatal(err)
	}
	never := seedSession(t, st, "never-classified", "", false, nil, map[classify.Class]int{classify.Prompt: 1})
	if _, err := st.W.Exec(`INSERT INTO host(host_uid, alias, is_local) VALUES ('ffff', 'lab', 0)`); err != nil {
		t.Fatal(err)
	}
	if _, err := st.W.Exec(`INSERT INTO session(host_uid, harness, profile, native_id, digest_only, derived_rev) VALUES ('ffff','codex','','remote-1',1,1)`); err != nil {
		t.Fatal(err)
	}
	res, err := d.Drain(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if res.Requeued != 6 || res.Pending != 6 || res.Written != 6 || res.Deferred != 0 {
		t.Fatalf("drain over a stale and an unclassified session: %+v", res)
	}
	var rev int64
	var body string
	if err := st.R.QueryRow(`SELECT input_rev, body FROM artifact WHERE sess_id=? AND kind=?`, stale, KindOutcome).Scan(&rev, &body); err != nil {
		t.Fatal(err)
	}
	if rev != 4 || !strings.Contains(body, "abandoned? (3 interrupts)") {
		t.Fatalf("the stale artifact must be re-derived from the new rows: input_rev %d %q", rev, body)
	}
	var n int
	if err := st.R.QueryRow(`SELECT count(*) FROM artifact WHERE sess_id=?`, never).Scan(&n); err != nil || n != 3 {
		t.Fatalf("unclassified session: %d artifacts %v", n, err)
	}
	if err := st.R.QueryRow(`SELECT count(*) FROM enrich_queue q JOIN session s ON s.sess_id=q.sess_id WHERE s.digest_only=1`).Scan(&n); err != nil || n != 0 {
		t.Fatalf("a pulled card must never be queued: %d %v", n, err)
	}
	// Fresh everywhere: the next drain finds nothing to requeue.
	if res, err = d.Drain(context.Background()); err != nil || res.Requeued != 0 || res.Pending != 0 {
		t.Fatalf("third drain: %+v %v", res, err)
	}
}

func TestInputHint_KeepsSpacesInValues(t *testing.T) {
	in := &Input{Hints: "purpose=conductor ops outcome=partially worked ticket=SB-1 empty="}
	for key, want := range map[string]string{"purpose": "conductor ops", "outcome": "partially worked", "ticket": "SB-1", "empty": "", "missing": ""} {
		if got := in.Hint(key); got != want {
			t.Errorf("Hint(%q) = %q want %q", key, got, want)
		}
	}
}
