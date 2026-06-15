package selfheal

import (
	"bufio"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/asheshgoplani/agent-deck/internal/tmux"
)

func TestNDJSONSink_AppendOnly_NeverTruncates(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "nested", "selfheal-audit.ndjson")
	sink, err := NewNDJSONSink(path)
	if err != nil {
		t.Fatalf("NewNDJSONSink: %v", err)
	}
	if sink.Path() != path {
		t.Fatalf("Path() = %q, want %q", sink.Path(), path)
	}

	for i := 0; i < 3; i++ {
		ev := Event{
			TS:        formatTS(time.Unix(int64(1780000000+i), 0)),
			SessionID: "s1",
			Substate:  string(tmux.SubstateModelUnavailable),
			Decision:  DecisionAct,
			WouldHave: ActionRestartModelSwitch,
			Stage:     ModeObserve,
		}
		if err := sink.Append(ev); err != nil {
			t.Fatalf("Append %d: %v", i, err)
		}
	}

	// A new sink to the same path must EXTEND, not truncate (durability across
	// process restarts — the ≥1-week observe window spans restarts).
	sink2, err := NewNDJSONSink(path)
	if err != nil {
		t.Fatalf("re-open sink: %v", err)
	}
	if err := sink2.Append(Event{SessionID: "s2", Stage: ModeObserve, Decision: DecisionSkipBusy}); err != nil {
		t.Fatalf("Append after re-open: %v", err)
	}

	f, err := os.Open(path)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer f.Close()
	var lines []Event
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		var e Event
		if err := json.Unmarshal(sc.Bytes(), &e); err != nil {
			t.Fatalf("bad NDJSON line %q: %v", sc.Text(), err)
		}
		lines = append(lines, e)
	}
	if len(lines) != 4 {
		t.Fatalf("want 4 durable records (3 + 1 after re-open), got %d", len(lines))
	}
	if lines[0].SessionID != "s1" || lines[3].SessionID != "s2" {
		t.Fatalf("append order/durability broken: %q ... %q", lines[0].SessionID, lines[3].SessionID)
	}
}

func TestNDJSONSink_EmptyPath_Errors(t *testing.T) {
	if _, err := NewNDJSONSink(""); err == nil {
		t.Fatal("empty path must error")
	}
}

func TestNDJSONSink_Concurrent(t *testing.T) {
	dir := t.TempDir()
	sink, err := NewNDJSONSink(filepath.Join(dir, "a.ndjson"))
	if err != nil {
		t.Fatal(err)
	}
	const n = 50
	done := make(chan struct{}, n)
	for i := 0; i < n; i++ {
		go func() {
			_ = sink.Append(Event{SessionID: "x", Stage: ModeObserve})
			done <- struct{}{}
		}()
	}
	for i := 0; i < n; i++ {
		<-done
	}
	f, _ := os.Open(sink.Path())
	defer f.Close()
	count := 0
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		count++
	}
	if count != n {
		t.Fatalf("concurrent appends: want %d lines, got %d", n, count)
	}
}

func TestEvent_JSONShape_MatchesSpec(t *testing.T) {
	ev := Event{
		TS:           "2026-06-15T11:00:00Z",
		SessionID:    "ab12-1780000000",
		Title:        "exec-fix-1414",
		Group:        "agent-deck",
		Profile:      "personal",
		Substate:     string(tmux.SubstateModelUnavailable),
		Dwell:        95,
		Reads:        []ReadSig{{T: "t1", Sig: "h1"}, {T: "t2", Sig: "h1"}},
		Decision:     DecisionAct,
		Caps:         CapsState{Session6h: 1, GlobalHour: 2},
		Outcome:      "observe_noop",
		Stage:        ModeObserve,
		WouldHave:    ActionRestartModelSwitch,
		ActionParams: map[string]any{"model": "opus", "reissue": true},
	}
	b, err := json.Marshal(ev)
	if err != nil {
		t.Fatal(err)
	}
	var m map[string]any
	if err := json.Unmarshal(b, &m); err != nil {
		t.Fatal(err)
	}
	// Spec §5 required keys present; "action" absent (observe takes none).
	for _, k := range []string{"ts", "session_id", "substate", "dwell_seconds", "reads", "decision", "caps", "stage", "would_have"} {
		if _, ok := m[k]; !ok {
			t.Errorf("event JSON missing required key %q", k)
		}
	}
	if _, ok := m["action"]; ok {
		t.Errorf("observe event must omit the taken-action field")
	}
}
