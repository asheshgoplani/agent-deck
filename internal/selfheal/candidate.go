package selfheal

import (
	"time"

	"github.com/asheshgoplani/agent-deck/internal/tmux"
)

// Candidate is a pure snapshot of one session's self-heal-relevant state for a
// single evaluation cycle. It is assembled by the daemon adapter from the data
// the poll loop already read (status, substate, hook freshness, output
// signatures, the dwell anchors). The policy never reaches back into tmux or the
// DB; everything it needs to decide is in here. This is what makes the predicate
// deterministic and the observe-only invariant structural.
type Candidate struct {
	// Identity (carried into the audit event).
	SessionID string
	Title     string
	Group     string
	Profile   string
	Account   string

	// Coarse status + additive substate (Honest Status v2, v1.9.66).
	Status   string
	Substate tmux.Substate

	// Busy is the canonical busy-first signal (tmux.go busy-first ordering). A
	// live busy indicator is AUTHORITATIVE and disqualifying (§1.3 #2 / §3.1).
	Busy bool

	// HookRunningFresh is true when a hook-status "running" was seen within the
	// freshness window (sessionstatus freshnessFor). Mid-turn → never act
	// (§1.3 #3 / §3.2).
	HookRunningFresh bool

	// OutputSig is a stable signature (hash) of the recent pane/output content
	// THIS read. OutputMoved is true when it differs from the previous read's
	// signature — token/output movement means mid-turn, disqualifying (§1.3 #3).
	OutputSig   string
	OutputMoved bool

	// Stopped marks a user-intentional stopped session (highest precedence,
	// §1.3 #5). Such a session is never a candidate.
	Stopped bool

	// OptedOut is the per-session or group-level "never self-heal me" flag,
	// checked as a quick disqualifier in the predicate (§3.7).
	OptedOut bool

	// StatusChangedAt anchors the dwell clock: when the current status/substate
	// was entered. Zero means unknown (treated as not-yet-dwelled).
	StatusChangedAt time.Time

	// LastSentAt is when self-heal/the keysender last delivered input to this
	// session. idle_at_empty_prompt only counts as stuck AFTER a send (§1.3 #4,
	// §1.4): the dwell for that class is measured from LastSentAt. Zero means
	// "we never sent it anything" → a long-waiting deliberate-idle session,
	// never a candidate.
	LastSentAt time.Time
}

// dwellAnchor returns the timestamp the dwell window is measured from for this
// candidate's substate, and whether an anchor exists at all.
//
//   - idle_at_empty_prompt is anchored on LastSentAt: it is only stuck if WE sent
//     something and nothing happened. No send → not a candidate (§1.4).
//   - every other stuck class is anchored on StatusChangedAt (when the banner /
//     stuck state was entered).
func (c Candidate) dwellAnchor() (time.Time, bool) {
	if c.Substate == tmux.SubstateIdleAtEmptyPrompt {
		if c.LastSentAt.IsZero() {
			return time.Time{}, false
		}
		return c.LastSentAt, true
	}
	if c.StatusChangedAt.IsZero() {
		return time.Time{}, false
	}
	return c.StatusChangedAt, true
}

// Dwell returns how long the candidate has dwelled in its stuck state as of now,
// and whether a dwell anchor exists.
func (c Candidate) Dwell(now time.Time) (time.Duration, bool) {
	anchor, ok := c.dwellAnchor()
	if !ok {
		return 0, false
	}
	return now.Sub(anchor), true
}

// PredicateResult is the outcome of evaluating the §1.3 stuck predicate against a
// single read of a candidate. It is intentionally verbose so the audit event can
// record exactly which condition decided the verdict.
type PredicateResult struct {
	// Candidate is true only when ALL §1.3 conditions hold for this read. The
	// two-read confirm is layered ON TOP by the Engine (one read is never enough,
	// §1.3 #4 / PLAYBOOK F5).
	Candidate bool
	// Decision is the most-precise reason the predicate reached its verdict. For
	// a true candidate it is DecisionAct (pending the 2-read confirm); for a
	// false one it names the disqualifier.
	Decision Decision
	// Dwell is the measured dwell at evaluation time (0 if no anchor).
	Dwell time.Duration
}

// Evaluate runs the §1.3 stuck predicate for ONE read. It is pure: same inputs →
// same verdict. The disqualifier ordering mirrors the design's precedence —
// stopped (user intent) and opt-out first as cheap exits, then the authoritative
// busy / mid-turn signals, then substate class, then dwell.
//
// A true result means "candidate for THIS read". The caller (Engine) must
// require two independent confirming reads before acting (§1.3 #4).
func Evaluate(c Candidate, now time.Time) PredicateResult {
	// 5 (precedence, cheap exits first): stopped = user-intentional, highest.
	if c.Stopped {
		return PredicateResult{Decision: DecisionSkipStopped}
	}
	// 5: opt-out is a quick disqualifier (§3.7).
	if c.OptedOut {
		return PredicateResult{Decision: DecisionSkipOptOut}
	}
	// 2: a live busy indicator is authoritative and disqualifying. An actively
	// running session is never stuck, full stop (§3.1).
	if c.Busy {
		return PredicateResult{Decision: DecisionSkipBusy}
	}
	// 3: mid-turn — a fresh hook-running or output movement between reads means
	// in-flight work. Never act mid-turn (§3.2).
	if c.HookRunningFresh || c.OutputMoved {
		return PredicateResult{Decision: DecisionSkipMidTurn}
	}
	// 1: substate must be a known-stuck class, never healthy.
	if !IsStuckSubstate(c.Substate) {
		return PredicateResult{Decision: DecisionSkipHealthy}
	}
	// 4: dwell past the cause-specific threshold, anchored on status_changed_at
	// or (for idle_at_empty_prompt) last_sent_at.
	threshold, _ := DwellThreshold(c.Substate)
	dwell, ok := c.Dwell(now)
	if !ok || dwell < threshold {
		return PredicateResult{Decision: DecisionSkipDwell, Dwell: dwell}
	}
	return PredicateResult{Candidate: true, Decision: DecisionAct, Dwell: dwell}
}
