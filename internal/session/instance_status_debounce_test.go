package session

import "testing"

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
