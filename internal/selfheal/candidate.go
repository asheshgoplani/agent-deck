package selfheal

import (
	"time"

	"github.com/asheshgoplani/agent-deck/internal/tmux"
)

// Candidate is one session's self-heal-relevant state for a single evaluation
// cycle. It is assembled by the daemon adapter from the data the poll loop
// already read (status, substate, hook freshness, output signatures, the dwell
// anchors).
//
// Every field except ComposerDraft is a plain value read before evaluation
// starts, which is what keeps Evaluate — the §1.3 stuck predicate — pure: same
// inputs, same verdict, no reach back into tmux or the DB.
//
// ComposerDraft is the one exception and it is deliberate. It is a live callback
// into `tmux capture-pane`, resolved by the Engine (in every mode, observe
// included) at the point the D6 hard precondition is decided, which is inside
// ProcessRead and after Evaluate has already returned. It sits outside the pure
// predicate precisely so the predicate stays pure; see the field's own doc for
// why the lookup is deferred rather than pre-read.
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

	// NotBefore blocks action until a known-future moment. Zero means no gate.
	//
	// It exists because a usage limit is not a dwell problem: the window reopens
	// at a wall-clock time hours away, and no dwell threshold can express "wait
	// until T". The caller derives it from the rejection's own reset string
	// (falling back to record + 20m), so the schedule is a hint and the observed
	// outcome remains the authority — a resume attempted at T either completes
	// the turn or produces a fresh rejection that rearms the gate.
	NotBefore time.Time

	// ComposerDraft reports whether the target's composer holds text the operator
	// typed. It is a hard precondition, not a preference: submitting someone
	// else's text is not a decision a status probe gets to make, and the
	// `session nudge --force` path is known to CONSUME an operator draft rather
	// than restore it (2026-08-07, conductor2-testfix, "target release-6.18.1").
	// The engine downgrades ActionResume to ActionEscalate when it reports true.
	//
	// It is a DEFERRED lookup rather than a bool because resolving it costs a
	// fresh `tmux capture-pane` subprocess (3s timeout) against the target pane,
	// and the daemon evaluates every wedged session on every 1-3s poll inside one
	// serial loop. A transport outage is correlated by construction, so a
	// resolve-per-read would fork one capture per wedged session per poll —
	// including for the reads that can only ever return skip_dwell / skip_confirm
	// and never consult it. That is the multi-second-freeze class this repo has
	// hit before.
	//
	// The engine consults it at exactly one point: where it holds a confirmed
	// candidate and is deciding whether to act — the same position in the
	// sequence the value was read from before, still BEFORE policy.RecordAttempt,
	// so a draft never spends one of the session's two 6-hour recoveries.
	//
	// That point is reached repeatedly, not once: it sits above the safety
	// machine, so a session whose breaker is open or whose cap is spent keeps
	// arriving there for as long as it stays wedged. The engine therefore
	// MEMOISES protective "draft present" answers per session with a short TTL
	// rather than re-forking a capture each time (see Engine.hasComposerDraft /
	// composerDraftTTL). Empty answers are deliberately fresh on every deciding
	// read, because the operator may begin typing between confirmed cycles.
	//
	// nil means the caller has no way to look, which reads as "no draft". The
	// fail-safe for a capture that ERRORS ("there might be a draft") belongs to
	// the lookup itself, since only the caller knows a capture failed — and the
	// engine relies on that: an errored capture must surface as true, never as
	// false, so it receives the same protective memo as a visible draft.
	ComposerDraft func() bool
}

// dwellAnchor returns the timestamp the dwell window is measured from for this
// candidate's substate, and whether an anchor exists at all.
//
//   - idle_at_empty_prompt is anchored on LastSentAt: it is only stuck if WE sent
//     something and nothing happened. No send → not a candidate (§1.4).
//   - every other stuck class — including api-error and usage-limit — is anchored
//     on StatusChangedAt (when the banner / stuck state was entered). For
//     api-error that is deliberate: the banner is direct positive evidence, so a
//     session nobody ever sent anything to is still eligible.
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
	// A scheduled wake: the candidate is genuinely stuck and has dwelled, but the
	// condition is known not to have cleared yet (a usage window that reopens at
	// a wall-clock time). Acting early burns one of the two per-session
	// recoveries in the 6h window on an attempt that cannot succeed, which is
	// exactly what the schedule exists to prevent.
	if !c.NotBefore.IsZero() && now.Before(c.NotBefore) {
		return PredicateResult{Decision: DecisionSkipNotBefore, Dwell: dwell}
	}
	return PredicateResult{Candidate: true, Decision: DecisionAct, Dwell: dwell}
}
