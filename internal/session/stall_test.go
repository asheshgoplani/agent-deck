package session

import (
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/asheshgoplani/agent-deck/internal/tmux"
)

// ---------------------------------------------------------------------------
// SubstateStalled.
//
// The live failure (2026-07-24): a supervisor session hit "API Error: Unable to
// connect to API (ConnectionRefused)" and wedged for an hour. Its pane kept
// rendering and kept accepting keystrokes — a nudge typed into it appeared in
// the composer — but Enter never submitted. Every content-only heuristic read
// it as idle-at-empty-prompt, so the watchdog kept "nudging" a session that
// could not receive anything, and agent-deck's own status flapped
// running<->waiting instead of reporting the wedge.
//
// These tests pin the discriminator: a composer draft that does not change
// while nothing runs.
// ---------------------------------------------------------------------------

// paneWithComposer renders a Claude-style pane whose composer holds text.
func paneWithComposer(text string) string {
	div := strings.Repeat("─", 40)
	return "prior output\n" + div + "\n❯ " + text + "\n" + div + "\n  bypass permissions on\n"
}

// fakePane serves canned captures to promoteStalled.
type fakePane struct {
	content string
	err     error
	calls   int
}

func (f *fakePane) CapturePaneFresh() (string, error) {
	f.calls++
	return f.content, f.err
}

// withStallClock pins the clock and dwell for deterministic tests.
func withStallClock(t *testing.T, now *time.Time) {
	t.Helper()
	origClock, origDwell := stallClock, StallDwell
	stallClock = func() time.Time { return *now }
	StallDwell = 10 * time.Minute
	t.Cleanup(func() { stallClock, StallDwell = origClock, origDwell })
}

func TestPromoteStalled_FrozenDraftBecomesStalled(t *testing.T) {
	now := time.Unix(1_700_000_000, 0)
	withStallClock(t, &now)

	pane := &fakePane{content: paneWithComposer("Auto-nudge from your monitor: resume supervising")}
	tracker := &stallTracker{}

	// First observation starts the clock — never stalled on sight, or a
	// composer glimpsed mid-type would trip it immediately.
	if got := promoteStalled(tmux.SubstateIdleAtEmptyPrompt, pane, tracker); got != tmux.SubstateIdleAtEmptyPrompt {
		t.Fatalf("first observation: want %q, got %q", tmux.SubstateIdleAtEmptyPrompt, got)
	}

	// Still inside the dwell.
	now = now.Add(9 * time.Minute)
	if got := promoteStalled(tmux.SubstateIdleAtEmptyPrompt, pane, tracker); got != tmux.SubstateIdleAtEmptyPrompt {
		t.Fatalf("inside dwell: want %q, got %q", tmux.SubstateIdleAtEmptyPrompt, got)
	}

	// Past the dwell with the same unchanged draft: wedged.
	now = now.Add(2 * time.Minute)
	if got := promoteStalled(tmux.SubstateIdleAtEmptyPrompt, pane, tracker); got != tmux.SubstateStalled {
		t.Fatalf("past dwell: want %q, got %q", tmux.SubstateStalled, got)
	}
}

func TestPromoteStalled_TypingOperatorNeverStalls(t *testing.T) {
	now := time.Unix(1_700_000_000, 0)
	withStallClock(t, &now)

	tracker := &stallTracker{}
	// A human composing a long prompt: the draft grows on every observation,
	// each of which restarts the clock. This is the false positive worth
	// spending state to avoid — a status probe must never call a thinking
	// operator "stalled".
	for _, draft := range []string{
		"check if",
		"check if the re-seed",
		"check if the re-seed actually",
		"check if the re-seed actually landed",
	} {
		now = now.Add(30 * time.Minute)
		pane := &fakePane{content: paneWithComposer(draft)}
		if got := promoteStalled(tmux.SubstateIdleAtEmptyPrompt, pane, tracker); got != tmux.SubstateIdleAtEmptyPrompt {
			t.Fatalf("draft %q after 30m: want %q, got %q", draft, tmux.SubstateIdleAtEmptyPrompt, got)
		}
	}
}

func TestPromoteStalled_EmptyComposerIsGenuinelyIdle(t *testing.T) {
	now := time.Unix(1_700_000_000, 0)
	withStallClock(t, &now)

	pane := &fakePane{content: paneWithComposer("")}
	tracker := &stallTracker{}

	for i := 0; i < 3; i++ {
		now = now.Add(time.Hour)
		if got := promoteStalled(tmux.SubstateIdleAtEmptyPrompt, pane, tracker); got != tmux.SubstateIdleAtEmptyPrompt {
			t.Fatalf("empty composer must stay idle, got %q", got)
		}
	}
}

// TestPromoteStalled_OnlyRefinesIdleVerdict pins the blast radius: a running,
// auth-failed or model-unavailable session is already described accurately and
// must pass through untouched, without even a pane read.
func TestPromoteStalled_OnlyRefinesIdleVerdict(t *testing.T) {
	now := time.Unix(1_700_000_000, 0)
	withStallClock(t, &now)

	for _, base := range []tmux.Substate{
		tmux.SubstateRunning,
		tmux.SubstateAuth401,
		tmux.SubstateModelUnavailable,
		tmux.SubstateNone,
	} {
		pane := &fakePane{content: paneWithComposer("frozen draft")}
		tracker := &stallTracker{}
		now = now.Add(time.Hour)
		if got := promoteStalled(base, pane, tracker); got != base {
			t.Errorf("base %q must pass through, got %q", base, got)
		}
		if pane.calls != 0 {
			t.Errorf("base %q must not cost a pane capture, got %d", base, pane.calls)
		}
	}
}

// TestPromoteStalled_ResumedWorkRestartsClock: a session that stalls, gets
// recovered, then parks a new draft must start a fresh clock rather than
// inheriting the old dwell.
func TestPromoteStalled_ResumedWorkRestartsClock(t *testing.T) {
	now := time.Unix(1_700_000_000, 0)
	withStallClock(t, &now)

	tracker := &stallTracker{}
	stalledPane := &fakePane{content: paneWithComposer("wedged message")}

	promoteStalled(tmux.SubstateIdleAtEmptyPrompt, stalledPane, tracker)
	now = now.Add(time.Hour)
	if got := promoteStalled(tmux.SubstateIdleAtEmptyPrompt, stalledPane, tracker); got != tmux.SubstateStalled {
		t.Fatalf("setup: expected a stall, got %q", got)
	}

	// Recovered: the session is running again. That resets the tracker.
	promoteStalled(tmux.SubstateRunning, stalledPane, tracker)

	// A new draft parked immediately afterwards is not instantly stalled.
	newPane := &fakePane{content: paneWithComposer("a fresh draft")}
	if got := promoteStalled(tmux.SubstateIdleAtEmptyPrompt, newPane, tracker); got != tmux.SubstateIdleAtEmptyPrompt {
		t.Fatalf("clock must restart after recovery, got %q", got)
	}
}

// TestPromoteStalled_CaptureErrorDoesNotResetDwell: a transient capture failure
// is not evidence of health, and must not wipe a dwell that is about to fire.
func TestPromoteStalled_CaptureErrorDoesNotResetDwell(t *testing.T) {
	now := time.Unix(1_700_000_000, 0)
	withStallClock(t, &now)

	tracker := &stallTracker{}
	good := &fakePane{content: paneWithComposer("wedged message")}
	bad := &fakePane{err: errors.New("capture-pane: connection lost")}

	promoteStalled(tmux.SubstateIdleAtEmptyPrompt, good, tracker)
	now = now.Add(30 * time.Minute)

	if got := promoteStalled(tmux.SubstateIdleAtEmptyPrompt, bad, tracker); got != tmux.SubstateIdleAtEmptyPrompt {
		t.Fatalf("capture error should report the base verdict, got %q", got)
	}
	// The dwell survived the failed capture.
	if got := promoteStalled(tmux.SubstateIdleAtEmptyPrompt, good, tracker); got != tmux.SubstateStalled {
		t.Fatalf("dwell must survive a capture error, got %q", got)
	}
}

// TestPromoteStalled_NilTrackerIsSafe covers instances whose tracker was never
// created (no substate path ever ran).
func TestPromoteStalled_NilTrackerIsSafe(t *testing.T) {
	pane := &fakePane{content: paneWithComposer("draft")}
	if got := promoteStalled(tmux.SubstateIdleAtEmptyPrompt, pane, nil); got != tmux.SubstateIdleAtEmptyPrompt {
		t.Fatalf("nil tracker must pass through, got %q", got)
	}
}

// TestStallTracker_StalledIsPaneFree pins the property the TUI render path
// depends on: the cached check reads only in-memory dwell state. An unobserved
// session must report false rather than going and looking, so adding stall
// awareness to the render loop costs nothing per row per tick.
func TestStallTracker_StalledIsPaneFree(t *testing.T) {
	now := time.Unix(1_700_000_000, 0)
	withStallClock(t, &now)

	// Never observed: nothing known, so nothing claimed.
	fresh := &stallTracker{}
	if fresh.stalled(now.Add(24 * time.Hour)) {
		t.Error("an unobserved session must never report stalled")
	}

	// Observed once, dwell not yet elapsed.
	tracker := &stallTracker{}
	pane := &fakePane{content: paneWithComposer("wedged message")}
	promoteStalled(tmux.SubstateIdleAtEmptyPrompt, pane, tracker)
	capturesAfterObserve := pane.calls

	if tracker.stalled(now.Add(time.Minute)) {
		t.Error("inside the dwell the cached check must report false")
	}
	if !tracker.stalled(now.Add(StallDwell + time.Second)) {
		t.Error("past the dwell the cached check must report true")
	}
	if pane.calls != capturesAfterObserve {
		t.Errorf("the cached check must not capture the pane: %d extra captures",
			pane.calls-capturesAfterObserve)
	}
}
