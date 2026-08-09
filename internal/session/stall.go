package session

import (
	"sync"
	"time"

	"github.com/asheshgoplani/agent-deck/internal/send"
	"github.com/asheshgoplani/agent-deck/internal/tmux"
)

// Stall detection (SubstateStalled).
//
// The gap this closes: every existing status heuristic reads a single pane
// capture, and a wedged Claude session is indistinguishable from a healthy one
// in a single frame. When a session's turn state machine fails to return to
// idle after a transport error, the pane keeps rendering and keeps ACCEPTING
// keystrokes — text typed into it appears normally — but Enter never submits.
// Content-only classification therefore reports SubstateIdleAtEmptyPrompt,
// which is exactly wrong: the prompt is not empty and the session is not idle.
//
// Only time distinguishes the two. The discriminator used here is dwell on an
// unchanging composer draft:
//
//   - composer holds text  + text keeps changing -> a human is typing, not stalled
//   - composer holds text  + text frozen for N   -> stalled
//   - composer empty                             -> genuinely idle
//   - anything running/erroring                  -> not our case, left alone
//
// This is reporting only. Nothing here recovers a session: the draft in a
// stalled composer may be an operator's, and submitting or clearing someone
// else's text is not a decision a status probe gets to make. Automatic
// Escape+Enter recovery lives in the send path, where the composer contents
// are known to be a message agent-deck itself just typed.

// StallDwell is how long a composer draft must sit unchanged, with no activity,
// before a session is reported as stalled.
//
// Sized to be unambiguous rather than fast: a human composing a long prompt
// pauses for minutes, and a slow tool call can leave a draft parked while real
// work happens elsewhere. The wedge this detects lasts until someone
// intervenes, so waiting is nearly free — the live incident this was built
// from sat wedged for an hour. False "stalled" on a thinking operator is worse
// than reporting a real wedge ten minutes late.
var StallDwell = 10 * time.Minute

// stallClock is the time source, swapped in tests.
var stallClock = time.Now

// stallTracker holds the per-instance dwell state for stall detection. Kept off
// the Instance struct (and therefore out of the persisted row) because it is
// derived, process-local, and meaningless across a restart.
type stallTracker struct {
	mu sync.Mutex
	// draft is the composer text observed at the last check.
	draft string
	// since is when the current draft was first seen unchanged. Zero means no
	// draft is being tracked.
	since time.Time
}

// observe folds one observation into the tracker and reports whether the
// session should now be considered stalled.
//
// draft is the current composer text ("" when the composer is empty or absent).
// A changed draft restarts the clock: that is a human typing, which is the
// single most likely false positive and the one worth spending state on.
func (t *stallTracker) observe(draft string, now time.Time) bool {
	t.mu.Lock()
	defer t.mu.Unlock()

	if draft == "" {
		t.draft = ""
		t.since = time.Time{}
		return false
	}
	if draft != t.draft {
		t.draft = draft
		t.since = now
		return false
	}
	if t.since.IsZero() {
		t.since = now
		return false
	}
	return now.Sub(t.since) >= StallDwell
}

// stalled reports whether the dwell has already elapsed, WITHOUT observing the
// pane. This is what the TUI render hot path reads: it is a pure in-memory time
// comparison over state some other caller already established, so a stalled
// session can be rendered honestly without adding a pane capture per row per
// tick (this repo has a history of exactly that becoming a UI freeze).
//
// When nothing has ever observed this session, the tracker is empty and this
// returns false — the render path then behaves precisely as it did before.
func (t *stallTracker) stalled(now time.Time) bool {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.draft == "" || t.since.IsZero() {
		return false
	}
	return now.Sub(t.since) >= StallDwell
}

// reset clears dwell state. Called whenever the session is observed in any
// state other than "idle at a prompt holding text", so a session that resumes
// work and later parks a new draft starts its clock fresh.
func (t *stallTracker) reset() {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.draft = ""
	t.since = time.Time{}
}

// stallSource is the minimal pane surface promoteStalled needs. *tmux.Session
// satisfies it.
type stallSource interface {
	CapturePaneFresh() (string, error)
}

// promoteStalled refines an already-computed substate with dwell information,
// returning SubstateStalled when a session that looks idle — or that is showing
// a transport-error banner — is in fact holding an unchanging composer draft.
//
// Two bases are eligible:
//
//   - SubstateIdleAtEmptyPrompt — the original case: content-only heuristics
//     cannot tell a wedged composer from a healthy empty prompt.
//   - SubstateAPIError — the SAME case, now classified one step earlier. This
//     substate is defined by precisely the banner this detector was built from
//     ("API Error: Unable to connect to API (ConnectionRefused)", 2026-07-24),
//     and it is checked ahead of the idle verdict, so refining only the idle
//     base would make SubstateStalled unreachable for the panes it exists to
//     describe.
//
// The split is load-bearing for recovery, not cosmetic:
//
//   - banner + EMPTY composer  -> stays api-error, and self-heal may deliver one
//     continuation prompt after its dwell; there is no operator text to destroy.
//   - banner + DRAFTED composer -> stalled, and `session nudge` REFUSES to send.
//     That refusal is the thing standing between an operator's in-flight draft
//     and a send that consumes it (the --force path is known to consume rather
//     than restore it). Submitting someone else's text is not a decision a
//     status probe gets to make.
//
// SubstateStalled remains reporting-only: it is deliberately absent from
// selfheal's stuckDwellThresholds and actionForSubstate, so promotion here can
// only ever protect a session, never schedule an action against it.
//
// A running, auth-failed or model-unavailable session is already being described
// accurately, and a session with no composer at all has nothing to stall on.
func promoteStalled(base tmux.Substate, src stallSource, tracker *stallTracker) tmux.Substate {
	if tracker == nil {
		return base
	}
	if base != tmux.SubstateIdleAtEmptyPrompt && base != tmux.SubstateAPIError {
		tracker.reset()
		return base
	}
	if src == nil {
		tracker.reset()
		return base
	}
	// Raw capture (ANSI intact): the SGR dim attribute is what separates
	// Claude's autosuggestion from real text, so ComposerDraft must see it.
	raw, err := src.CapturePaneFresh()
	if err != nil {
		// A capture failure is not evidence of a stall. Leave the dwell clock
		// untouched so a transient error does not reset a real one.
		return base
	}
	draft, visible := send.ComposerDraft(raw, tmux.StripANSI)
	if !visible {
		tracker.reset()
		return base
	}
	if tracker.observe(draft, stallClock()) {
		return tmux.SubstateStalled
	}
	return base
}
