package session

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// Issue #2900: some Codex turn ends produced no running -> waiting record, so
// the parent was never woken. These tests drive the daemon's emission paths
// in the order syncProfile runs them, for a Codex child whose hook evidence
// (the notify writer's completion generation) does or does not advance, and
// assert the contract: every real turn end yields exactly one record.

type codexTurnFixture struct {
	d      *TransitionDaemon
	parent *Instance
	child  *Instance
	byID   map[string]*Instance
	prev   map[string]string
	idle   bool
}

func newCodexTurnFixture(t *testing.T, tool string) *codexTurnFixture {
	t.Helper()
	storage := reviewTestHome(t, "default")
	project := t.TempDir()
	parent := NewInstanceWithTool("conductor-test", project, "claude")
	parent.ID = "parent-2900"
	parent.Status = StatusWaiting
	child := NewInstanceWithTool("codex-worker", project, tool)
	child.ID = "child-2900"
	child.Tool = tool
	child.ParentSessionID = parent.ID
	child.CodexSessionID = "019a0000-0000-7000-8000-000000002900"
	child.Status = StatusWaiting
	if err := storage.SaveWithGroups([]*Instance{parent, child}, nil); err != nil {
		t.Fatalf("SaveWithGroups: %v", err)
	}
	if err := os.MkdirAll(GetHooksDir(), 0o700); err != nil {
		t.Fatal(err)
	}
	n := NewTransitionNotifier()
	n.wake = &wakeNudgeWiring{
		nudger: NewWakeNudger(0),
		now:    time.Now,
		isIdle: func(*Instance, string) bool { return true },
		send:   func(*Instance, string, string) error { return nil },
	}
	f := &codexTurnFixture{
		parent: parent, child: child,
		byID: map[string]*Instance{parent.ID: parent, child.ID: child},
		prev: map[string]string{}, idle: true,
	}
	f.d = &TransitionDaemon{
		notifier:      n,
		lastStatus:    map[string]map[string]string{},
		initialized:   map[string]bool{},
		lastDone:      map[string]map[string]DoneSignal{},
		lastTurn:      map[string]map[string]string{},
		lastDoneScan:  map[string]map[string]time.Time{},
		turnLiveCheck: func(*Instance) bool { return true },
	}
	f.d.codexComposerIdle = func(*Instance) bool { return f.idle }
	return f
}

// writeHook writes the hook status file the codex-notify writer leaves after
// a completed turn: status waiting, the turn's generation on both sides.
func (f *codexTurnFixture) writeHook(t *testing.T, turnID string, at time.Time) {
	t.Helper()
	gen := f.child.CodexSessionID + ":" + turnID
	b, err := json.Marshal(map[string]any{
		"status":                     "waiting",
		"session_id":                 f.child.CodexSessionID,
		"event":                      "agent-turn-complete",
		"ts":                         at.Unix(),
		"codex_started_generation":   gen,
		"codex_completed_generation": gen,
		"codex_started_session_id":   f.child.CodexSessionID,
		"codex_completed_session_id": f.child.CodexSessionID,
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(GetHooksDir(), f.child.ID+".json"), b, 0o600); err != nil {
		t.Fatal(err)
	}
}

// pass runs one daemon pass with the child at status, in syncProfile's order:
// observe runs, recorded turns, snapshot edges, hook candidates, fallback.
func (f *codexTurnFixture) pass(t *testing.T, status string, now time.Time) {
	t.Helper()
	const profile = "default"
	statuses := map[string]string{f.child.ID: status, f.parent.ID: "waiting"}
	f.d.observeCodexRuns(profile, f.byID, statuses, now)
	f.d.recordTerminalTurns(profile, f.byID, statuses, nil)
	for id, to := range statuses {
		if from := f.prev[id]; ShouldNotifyTransition(from, to) {
			ev, ok := f.d.emitTurn(profile, f.byID[id], f.byID, from, to, now, true)
			t.Logf("snapshot edge %s %s->%s: ok=%v result=%q reason=%q hash=%q", id, from, to, ok, ev.DeliveryResult, ev.DeadLetterReason, ev.LastOutputHash)
		}
	}
	candidates := map[string]hookTransitionCandidate{}
	if hs := readHookStatusFile(f.child.ID); hs != nil {
		if c, ok := terminalHookTransitionCandidate(f.child.Tool, hs); ok {
			candidates[f.child.ID] = c
		}
	}
	f.d.emitHookTransitionCandidates(profile, f.byID, f.prev, statuses, candidates)
	f.d.emitCodexTurnFallbacks(profile, f.byID, statuses, true, now)
	f.prev = statuses
}

func (f *codexTurnFixture) records(t *testing.T) int {
	t.Helper()
	events, err := ReadInboxEvents(f.parent.ID)
	if err != nil {
		t.Fatalf("ReadInboxEvents: %v", err)
	}
	return len(events)
}

// A turn whose notify never lands (dropped as unbacked or subagent, a lost
// writer lock, an unreadable status file) left the hook on the previous turn's
// generation. Its observed running -> waiting edge carried that turn's signal,
// and the notifier dropped it as a duplicate for two hours.
func TestIssue2900_CodexObservedTurnWithStaleHookIsRecorded(t *testing.T) {
	f := newCodexTurnFixture(t, "codex")
	t0 := time.Now()
	f.pass(t, "running", t0)
	f.writeHook(t, "turn-a", t0.Add(5*time.Second))
	f.pass(t, "waiting", t0.Add(6*time.Second))
	if got := f.records(t); got != 1 {
		t.Fatalf("turn A: %d records, want 1", got)
	}

	// Turn B, ten minutes later; its notify never reaches the hook file.
	t1 := t0.Add(10 * time.Minute)
	f.pass(t, "running", t1)
	f.pass(t, "waiting", t1.Add(20*time.Second))
	if got := f.records(t); got != 2 {
		t.Fatalf("turn B with a stale hook: %d records, want 2 (the turn end was swallowed)", got)
	}
	// Re-observations of the parked child never add a record.
	for i := 0; i < 5; i++ {
		f.pass(t, "waiting", t1.Add(time.Duration(21+i)*time.Second))
	}
	f.pass(t, "idle", t1.Add(40*time.Second))
	if got := f.records(t); got != 2 {
		t.Fatalf("re-observing turn B: %d records, want 2", got)
	}
}

// With no hook evidence at all (empty signal: the record judged foreign under
// the wrong Codex home, or the consumed generation masked), two turns ending
// within 90 seconds collapsed into one under the notifier's legacy window.
func TestIssue2900_CodexTurnsWithoutHookSignalWithin90sBothRecorded(t *testing.T) {
	f := newCodexTurnFixture(t, "codex")
	t0 := time.Now()
	f.pass(t, "running", t0)
	f.pass(t, "waiting", t0.Add(10*time.Second))
	f.pass(t, "running", t0.Add(20*time.Second))
	f.pass(t, "waiting", t0.Add(40*time.Second))
	if got := f.records(t); got != 2 {
		t.Fatalf("two observed turns 30s apart with no hook signal: %d records, want 2", got)
	}
}

// The notify of turn B lands after the daemon already recorded B's edge under
// the run signal. The late generation is the same turn, not a new one.
func TestIssue2900_CodexLateNotifyDoesNotDoubleFire(t *testing.T) {
	f := newCodexTurnFixture(t, "codex")
	t0 := time.Now()
	f.pass(t, "running", t0)
	f.writeHook(t, "turn-a", t0)
	f.pass(t, "waiting", t0.Add(5*time.Second))

	t1 := t0.Add(10 * time.Minute)
	f.pass(t, "running", t1)
	f.pass(t, "waiting", t1.Add(10*time.Second))
	if got := f.records(t); got != 2 {
		t.Fatalf("turn B edge: %d records, want 2", got)
	}
	f.writeHook(t, "turn-b", time.Now()) // fresh: a hook candidate for 45 s
	for i := 0; i < 10; i++ {
		f.pass(t, "waiting", t1.Add(time.Duration(11+i)*time.Second))
	}
	f.pass(t, "idle", t1.Add(30*time.Second))
	if got := f.records(t); got != 2 {
		t.Fatalf("late notify of turn B: %d records, want still 2 (double fire)", got)
	}

	// The next real turn is recorded again.
	t2 := t1.Add(10 * time.Minute)
	f.pass(t, "running", t2)
	f.writeHook(t, "turn-c", time.Now())
	f.pass(t, "waiting", t2.Add(10*time.Second))
	if got := f.records(t); got != 3 {
		t.Fatalf("turn C: %d records, want 3", got)
	}
}

// The normal path: the notify lands before or with the composer, the hook
// candidate and the snapshot edge both see the turn, and the fallback stays
// quiet. One record per turn.
func TestIssue2900_CodexNormalTurnIsOneRecordAndFallbackStaysQuiet(t *testing.T) {
	f := newCodexTurnFixture(t, "codex")
	t0 := time.Now()
	f.pass(t, "running", t0)
	f.writeHook(t, "turn-a", time.Now())
	f.pass(t, "running", t0.Add(time.Second)) // the hook lands while tmux still says running
	f.pass(t, "waiting", t0.Add(2*time.Second))
	f.pass(t, "waiting", t0.Add(20*time.Second)) // past the fallback delay
	f.pass(t, "waiting", t0.Add(40*time.Second))
	if got := f.records(t); got != 1 {
		t.Fatalf("normal turn: %d records, want exactly 1", got)
	}
}

// The fallback: a run the daemon saw, then the child parked at its composer
// with no record from any path (the snapshot edge was missed: running ->
// waiting seen only through a stale registry row, no fresh hook). After the
// fallback delay, with the pane idle at its composer, exactly one record.
func TestIssue2900_CodexFallbackEmitsOnceWhenNoPathRecordedTheTurn(t *testing.T) {
	f := newCodexTurnFixture(t, "codex")
	t0 := time.Now()
	f.writeHook(t, "turn-a", t0.Add(-10*time.Minute)) // previous turn, not fresh
	f.pass(t, "waiting", t0.Add(-time.Minute))        // turn A, first sight
	if got := f.records(t); got != 1 {
		t.Fatalf("turn A: %d records, want 1", got)
	}
	f.pass(t, "running", t0)
	f.prev[f.child.ID] = "waiting" // turn B's edge was never observed
	f.idle = false                 // pane not at the composer yet
	f.pass(t, "waiting", t0.Add(2*time.Second))
	f.pass(t, "waiting", t0.Add(15*time.Second))
	if got := f.records(t); got != 1 {
		t.Fatalf("pane not at its composer: %d records, want 1", got)
	}
	f.idle = true
	f.pass(t, "waiting", t0.Add(4*time.Second)) // before the fallback delay
	if got := f.records(t); got != 1 {
		t.Fatalf("before the fallback delay: %d records, want 1", got)
	}
	f.pass(t, "waiting", t0.Add(16*time.Second))
	if got := f.records(t); got != 2 {
		t.Fatalf("fallback: %d records, want 2 (turn B lost)", got)
	}
	for i := 0; i < 5; i++ {
		f.pass(t, "waiting", t0.Add(time.Duration(17+i)*time.Second))
	}
	if got := f.records(t); got != 2 {
		t.Fatalf("fallback must fire once per run: %d records", got)
	}
}

// A custom tool wrapping Codex writes the same notify hook file, but the hook
// candidate and the turn signal were keyed on the literal tool name "codex".
func TestIssue2900_CodexCompatibleCustomToolGetsHookCandidateAndSignal(t *testing.T) {
	f := newCodexTurnFixture(t, "my-codex")
	restore := resetUserConfigCache(t, &UserConfig{Tools: map[string]ToolDef{
		"my-codex": {Command: "codex-wrapper", CompatibleWith: "codex"},
	}})
	t.Cleanup(restore)
	f.writeHook(t, "turn-a", time.Now())
	hs := readHookStatusFile(f.child.ID)
	if _, ok := terminalHookTransitionCandidate(f.child.Tool, hs); !ok {
		t.Fatal("a codex-compatible tool's fresh turn-complete hook yields no transition candidate")
	}
	if sig := codexTurnSignal(f.child); sig == "" {
		t.Fatal("a codex-compatible tool has no codex turn signal")
	}
}

func TestIssue2900_CodexContentAtComposer(t *testing.T) {
	cases := []struct {
		name    string
		content string
		want    bool
	}{
		{"idle composer", "• Done. The tests pass.\n\n› Ask Codex to do anything\n\n  ? for shortcuts            100% context left\n", true},
		{"working", "• Running tests\n\n◦ Working (12s • esc to interrupt)\n\n› \n", false},
		{"no prompt", "Loading codex...\n", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := codexContentAtComposer("codex", tc.content); got != tc.want {
				t.Fatalf("codexContentAtComposer = %v, want %v", got, tc.want)
			}
		})
	}
}
