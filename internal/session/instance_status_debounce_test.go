package session

import (
	"encoding/json"
	"os"
	"path/filepath"
	"syscall"
	"testing"
	"time"
)

func TestShouldDebounceTmuxFlipForTool(t *testing.T) {
	tests := map[string]bool{
		"":         true,
		"claude":   true,
		"codex":    true,
		"gemini":   true,
		"hermes":   true,
		"cursor":   true,
		"pi":       false,
		"shell":    false,
		"opencode": false,
	}
	for tool, want := range tests {
		if got := shouldDebounceTmuxFlipForTool(tool); got != want {
			t.Errorf("shouldDebounceTmuxFlipForTool(%q) = %v, want %v", tool, got, want)
		}
	}
}

// debounceFlipFromRunning gates a purely tmux-inferred flip away from running so
// a single transient sample (long tool-call past the hook freshness window, or a
// CapturePane failure during subprocess churn) does not fire a false
// completion/error to the conductor. These pin the one-tick-hold-then-flip
// behavior and the non-debounceable terminal signals.
func TestDebounceFlipFromRunning(t *testing.T) {
	cases := []struct {
		name        string
		prev        Status
		derived     Status
		tmuxRaw     string
		hookStatus  string
		pending     bool
		wantApply   Status
		wantPending bool
		wantHeld    bool
	}{
		{
			name: "first running->waiting sample is HELD at running",
			prev: StatusRunning, derived: StatusWaiting, tmuxRaw: "waiting", pending: false,
			wantApply: StatusRunning, wantPending: true, wantHeld: true,
		},
		{
			name: "second consecutive running->waiting sample FLIPS",
			prev: StatusRunning, derived: StatusWaiting, tmuxRaw: "waiting", pending: true,
			wantApply: StatusWaiting, wantPending: false, wantHeld: false,
		},
		{
			name: "first running->error (banner) sample is HELD",
			prev: StatusRunning, derived: StatusError, tmuxRaw: "error", pending: false,
			wantApply: StatusRunning, wantPending: true, wantHeld: true,
		},
		{
			name: "transient capture error (no raw) is HELD on first sample",
			prev: StatusRunning, derived: StatusError, tmuxRaw: "", pending: false,
			wantApply: StatusRunning, wantPending: true, wantHeld: true,
		},
		{
			name: "genuinely dead pane (inactive) is NEVER debounced",
			prev: StatusRunning, derived: StatusError, tmuxRaw: "inactive", pending: false,
			wantApply: StatusError, wantPending: false, wantHeld: false,
		},
		{
			name: "dead hook is NEVER debounced",
			prev: StatusRunning, derived: StatusError, tmuxRaw: "error", hookStatus: "dead", pending: false,
			wantApply: StatusError, wantPending: false, wantHeld: false,
		},
		{
			name: "flip not FROM running is not debounced",
			prev: StatusWaiting, derived: StatusError, tmuxRaw: "error", pending: false,
			wantApply: StatusError, wantPending: false, wantHeld: false,
		},
		{
			name: "running->running (recovered) clears, not held",
			prev: StatusRunning, derived: StatusRunning, tmuxRaw: "active", pending: true,
			wantApply: StatusRunning, wantPending: false, wantHeld: false,
		},
		{
			name: "running->idle is not a debounceable flip",
			prev: StatusRunning, derived: StatusIdle, tmuxRaw: "idle", pending: false,
			wantApply: StatusIdle, wantPending: false, wantHeld: false,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			apply, nextPending, held := debounceFlipFromRunning(tc.prev, tc.derived, tc.tmuxRaw, tc.hookStatus, tc.pending)
			if apply != tc.wantApply || nextPending != tc.wantPending || held != tc.wantHeld {
				t.Fatalf("debounceFlipFromRunning(prev=%s derived=%s raw=%q hook=%q pending=%v) = (%s,%v,%v); want (%s,%v,%v)",
					tc.prev, tc.derived, tc.tmuxRaw, tc.hookStatus, tc.pending,
					apply, nextPending, held, tc.wantApply, tc.wantPending, tc.wantHeld)
			}
		})
	}
}

func TestCodexCompletionConvergenceRequiresMatchingGenerationAndSession(t *testing.T) {
	tests := []struct {
		name, started, completed, startedSession, completedSession, bound string
		want                                                              bool
	}{
		{"matching", "g1", "g1", "s1", "s1", "s1", true},
		{"missing completion", "g1", "", "s1", "", "s1", false},
		{"mismatched completion", "g2", "g1", "s1", "s1", "s1", false},
		{"newer start supersedes completion", "g2", "g1", "s1", "s1", "s1", false},
		{"foreign session", "g1", "g1", "s1", "s1", "s2", false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			i := &Instance{Tool: "codex", CodexSessionID: tc.bound,
				codexStartedGeneration: tc.started, codexCompletedGeneration: tc.completed,
				codexStartedSessionID: tc.startedSession, codexCompletedSessionID: tc.completedSession}
			if got := i.codexCompletionConverged(); got != tc.want {
				t.Fatalf("codexCompletionConverged() = %v, want %v", got, tc.want)
			}
		})
	}
}

func TestCodexCompletionBypassesWaitingOnly(t *testing.T) {
	i := &Instance{Tool: "codex", CodexSessionID: "s",
		codexStartedGeneration: "g", codexCompletedGeneration: "g",
		codexStartedSessionID: "s", codexCompletedSessionID: "s"}
	if !i.shouldBypassCodexWaitingDebounce(StatusWaiting) {
		t.Fatal("converged completion must bypass waiting debounce")
	}
	if i.shouldBypassCodexWaitingDebounce(StatusError) {
		t.Fatal("completion evidence must not bypass error debounce")
	}
}

func TestSetCodexGenerationEvidence_EvidenceLessDoesNotErase(t *testing.T) {
	i := &Instance{Tool: "codex", codexStartedGeneration: "g", codexCompletedGeneration: "g",
		codexStartedSessionID: "s", codexCompletedSessionID: "s"}
	i.setCodexGenerationEvidence(&HookStatus{Status: "waiting"})
	if i.codexStartedGeneration != "g" || i.codexCompletedGeneration != "g" ||
		i.codexStartedSessionID != "s" || i.codexCompletedSessionID != "s" {
		t.Fatalf("evidence-less update erased valid evidence: %#v", i)
	}
}

func TestCodexCompletionEvidenceInvalidatedBySubsequentRunningTurn(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	instanceID := "codex-next-turn"
	sessionID := "thread-1"
	generation := sessionID + ":turn-a"
	path := filepath.Join(GetHooksDir(), instanceID+".json")
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	record := map[string]any{
		"status":                     "waiting",
		"session_id":                 sessionID,
		"event":                      "agent-turn-complete",
		"ts":                         time.Now().Unix(),
		"codex_started_generation":   generation,
		"codex_completed_generation": generation,
		"codex_started_session_id":   sessionID,
		"codex_completed_session_id": sessionID,
	}
	data, err := json.Marshal(record)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}

	i := &Instance{ID: instanceID, Tool: "codex", CodexSessionID: sessionID,
		codexStartedGeneration: generation, codexCompletedGeneration: generation,
		codexStartedSessionID: sessionID, codexCompletedSessionID: sessionID}
	if !i.codexCompletionConverged() {
		t.Fatal("completed turn A must initially converge")
	}

	// Turn B has no start hook; tmux is the authoritative newer-running edge.
	// Hold the host-only consumption lock and prove invalidation releases i.mu
	// while its bounded retry runs. Contention suppresses convergence but keeps
	// the generation in memory for the next running sample to retry.
	consumedDir := filepath.Join(GetHooksDir(), ".codex-consumed")
	if err := os.MkdirAll(consumedDir, 0o700); err != nil {
		t.Fatal(err)
	}
	lockPath := filepath.Join(consumedDir, instanceID+".lock")
	lock, err := os.OpenFile(lockPath, os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	if err := syscall.Flock(int(lock.Fd()), syscall.LOCK_EX); err != nil {
		t.Fatal(err)
	}
	i.mu.Lock()
	done := make(chan struct{})
	go func() {
		i.invalidateCodexCompletionOnRunning()
		i.mu.Unlock()
		close(done)
	}()
	muLive := make(chan Status)
	go func() {
		i.mu.Lock()
		status := i.Status
		i.mu.Unlock()
		muLive <- status
	}()
	select {
	case <-muLive:
	case <-time.After(250 * time.Millisecond):
		t.Fatal("Instance.mu stayed blocked behind the held file lock")
	}
	select {
	case <-done:
	case <-time.After(250 * time.Millisecond):
		t.Fatal("bounded file-lock acquisition did not return")
	}
	if i.codexCompletionConverged() {
		t.Fatal("contended consume must suppress stale completion convergence")
	}
	if i.codexStartedGeneration != generation || i.codexCompletedGeneration != generation {
		t.Fatal("contended durable consume discarded the retry generation")
	}
	if err := syscall.Flock(int(lock.Fd()), syscall.LOCK_UN); err != nil {
		t.Fatal(err)
	}
	if err := lock.Close(); err != nil {
		t.Fatal(err)
	}

	i.mu.Lock()
	i.invalidateCodexCompletionOnRunning()
	i.mu.Unlock()
	if i.codexCompletionConverged() {
		t.Fatal("stale turn A evidence converged after turn B started")
	}
	got := readHookStatusFile(instanceID)
	if got == nil {
		t.Fatal("hook status record disappeared")
	}
	if got.CodexStartedGeneration != "" || got.CodexCompletedGeneration != "" ||
		got.CodexStartedSessionID != "" || got.CodexCompletedSessionID != "" {
		t.Fatalf("durable stale completion evidence was not consumed: %#v", got)
	}

	// The watcher cached the unmasked hook record before consumption and sees
	// no event when the host-only marker lands. Polling must re-mask that cached
	// copy instead of rehydrating turn A into the warm Instance.
	watcher := &StatusFileWatcher{statuses: map[string]*HookStatus{instanceID: {
		Status: "waiting", SessionID: sessionID,
		CodexStartedGeneration: generation, CodexCompletedGeneration: generation,
		CodexStartedSessionID: sessionID, CodexCompletedSessionID: sessionID,
	}}}
	i.UpdateHookStatus(watcher.GetHookStatus(instanceID))
	if i.codexCompletionConverged() {
		t.Fatal("polling loop rehydrated consumed completion from watcher cache")
	}
}

func TestCodexCompletionInvalidationRetriesAfterDurableWriteFailure(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	instanceID, generation := "codex-retry", "thread-1:turn-a"
	consumedPath := codexConsumedGenerationPath(instanceID, generation)
	if err := os.MkdirAll(consumedPath, 0o700); err != nil { // force atomic rename failure
		t.Fatal(err)
	}
	i := &Instance{ID: instanceID, Tool: "codex", CodexSessionID: "thread-1",
		codexStartedGeneration: generation, codexCompletedGeneration: generation,
		codexStartedSessionID: "thread-1", codexCompletedSessionID: "thread-1"}

	i.mu.Lock()
	i.invalidateCodexCompletionOnRunning()
	i.mu.Unlock()
	if i.codexStartedGeneration != generation || i.codexCompletedGeneration != generation {
		t.Fatal("memory cleared before durable consume succeeded")
	}
	if i.codexCompletionConverged() {
		t.Fatal("failed durable consume must remain in the safe, non-converged state")
	}
	if err := os.Remove(consumedPath); err != nil {
		t.Fatal(err)
	}
	i.mu.Lock()
	i.invalidateCodexCompletionOnRunning()
	i.mu.Unlock()
	if i.codexStartedGeneration != "" || i.codexCompletedGeneration != "" {
		t.Fatal("retry did not clear memory after durable consume succeeded")
	}
}

func TestCodexConsumedGenerationsCannotOverwriteEachOther(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	instanceID := "codex-ordered-consume"
	newer := "thread-1:turn-b"
	older := "thread-1:turn-a"
	if consumed, err := consumeCodexCompletionEvidence(instanceID, newer); err != nil || !consumed {
		t.Fatalf("consume newer generation: consumed=%v err=%v", consumed, err)
	}
	// Simulate a delayed process persisting stale A after B already landed.
	if consumed, err := consumeCodexCompletionEvidence(instanceID, older); err != nil || !consumed {
		t.Fatalf("consume older generation: consumed=%v err=%v", consumed, err)
	}
	if !codexCompletionEvidenceConsumed(instanceID, newer) || !codexCompletionEvidenceConsumed(instanceID, older) {
		t.Fatal("delayed stale consume replaced another consumed generation")
	}
}

// The canonical long-bash sequence: running, then two consecutive tmux "waiting"
// reads. The first is held at running (no false completion), the second flips.
func TestDebounceFlipFromRunning_LongBashSequence(t *testing.T) {
	pending := false
	// Tick 1: long tool-call, hook window lapsed, pane shows a prompt.
	apply, pending, held := debounceFlipFromRunning(StatusRunning, StatusWaiting, "waiting", "", pending)
	if !held || apply != StatusRunning {
		t.Fatalf("tick 1 must hold at running; got apply=%s held=%v", apply, held)
	}
	// Tick 2: still waiting → confirmed flip.
	apply, pending, held = debounceFlipFromRunning(StatusRunning, StatusWaiting, "waiting", "", pending)
	if held || apply != StatusWaiting || pending {
		t.Fatalf("tick 2 must flip to waiting; got apply=%s held=%v pending=%v", apply, held, pending)
	}
}

// If the pane recovers on the second sample, no false flip ever surfaced.
func TestDebounceFlipFromRunning_RecoversAfterHold(t *testing.T) {
	// Tick 1: transient waiting → held.
	_, pending, held := debounceFlipFromRunning(StatusRunning, StatusWaiting, "waiting", "", false)
	if !held || !pending {
		t.Fatalf("tick 1 must hold; held=%v pending=%v", held, pending)
	}
	// Tick 2: back to active → cleared, never flipped.
	apply, pending, held := debounceFlipFromRunning(StatusRunning, StatusRunning, "active", "", pending)
	if held || apply != StatusRunning || pending {
		t.Fatalf("tick 2 recovery must clear without flip; apply=%s held=%v pending=%v", apply, held, pending)
	}
}
