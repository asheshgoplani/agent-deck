package selfheal

import (
	"sync"
	"time"

	"github.com/asheshgoplani/agent-deck/internal/tmux"
)

// Caps holds the §3.3 rate limits. Defaults mirror the rails agent-deck already
// trusts: the global hourly cap is the TriageMaxPerHour=5 precedent.
type Caps struct {
	// PerSession6h is the max recoveries per session per rolling 6h window
	// (default 2). auth_401 is special-cased to 1 (PerSessionAuth401).
	PerSession6h int
	// PerSessionAuth401 is the per-session cap for the auth_401 class (default 1):
	// a 401 that survives one restart trips the breaker immediately (§2.2/§3.4).
	PerSessionAuth401 int
	// GlobalPerHour is the max recoveries across the WHOLE fleet per hour
	// (default 5 = TriageMaxPerHour). On hitting it → escalate-only, one
	// consolidated incident (§3.3).
	GlobalPerHour int
	// BreakerK is the consecutive-failed-recovery count that opens the per-session
	// circuit breaker (default 2; 1 for auth_401, BreakerKAuth401) (§3.4).
	BreakerK         int
	BreakerKAuth401  int
}

// DefaultCaps returns the §3.3/§7 starting dials (tuned later from observe data).
func DefaultCaps() Caps {
	return Caps{
		PerSession6h:      2,
		PerSessionAuth401: 1,
		GlobalPerHour:     5,
		BreakerK:          2,
		BreakerKAuth401:   1,
	}
}

// CapsState is a read-only view of the cap counters for one decision, recorded in
// the audit event's "caps" field.
type CapsState struct {
	Session6h   int `json:"session_6h"`
	GlobalHour  int `json:"global_hour"`
	BreakerFails int `json:"breaker_fails"`
	BreakerOpen bool `json:"breaker_open"`
}

// recovery is a single recorded would-be recovery attempt (timestamped for the
// rolling windows). In observe mode no real recovery executes, but the
// state-machine still RECORDS would-be attempts so caps/backoff/breaker are
// exercised and their decisions are logged (§ Stage 1 brief item 4).
type recovery struct {
	at      time.Time
	substate tmux.Substate
}

// PolicyMachine is the deterministic safety state-machine (§3): caps, backoff,
// circuit breaker, and FlickerDetector subscription. It is the SAME machine in
// every stage; in observe mode it is exercised and logged but never gates a real
// action (because no action runs). Safe for concurrent use.
type PolicyMachine struct {
	caps Caps

	mu sync.Mutex
	// perSession holds recent would-be recoveries per session id (for the rolling
	// per-session window + backoff anchor).
	perSession map[string][]recovery
	// global holds recent would-be recoveries across the fleet (rolling hour).
	global []recovery
	// consecutiveFails counts consecutive FAILED recoveries per session (breaker).
	consecutiveFails map[string]int
	// quarantined marks sessions whose breaker is open (manual clear / self-heal).
	quarantined map[string]bool
	// flickering is the set of session ids the FlickerDetector flagged (>3
	// transitions / 60s) — a flapping session is by definition not safely
	// healable, so it is quarantine-equivalent (§3.4).
	flickering map[string]bool
}

// NewPolicyMachine builds a machine with the given caps (use DefaultCaps()).
func NewPolicyMachine(caps Caps) *PolicyMachine {
	return &PolicyMachine{
		caps:             caps,
		perSession:       map[string][]recovery{},
		consecutiveFails: map[string]int{},
		quarantined:      map[string]bool{},
		flickering:       map[string]bool{},
	}
}

// rolling windows for the two caps.
const (
	perSessionWindow = 6 * time.Hour
	globalWindow     = 1 * time.Hour
)

// SetFlickering records the FlickerDetector verdict for a session. A flapping
// session is quarantine-equivalent: self-heal must never restart into a flap
// (the #1349 / duplicate-killer family, §3.4/§3.5).
func (p *PolicyMachine) SetFlickering(sessionID string, flickering bool) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if flickering {
		p.flickering[sessionID] = true
	} else {
		delete(p.flickering, sessionID)
	}
}

// breakerLimit returns the breaker K for the given substate.
func (p *PolicyMachine) breakerLimit(s tmux.Substate) int {
	if s == tmux.SubstateAuth401 {
		return p.caps.BreakerKAuth401
	}
	return p.caps.BreakerK
}

// perSessionLimit returns the per-session recovery cap for the given substate.
func (p *PolicyMachine) perSessionLimit(s tmux.Substate) int {
	if s == tmux.SubstateAuth401 {
		return p.caps.PerSessionAuth401
	}
	return p.caps.PerSession6h
}

// prune drops recoveries outside their rolling window. Caller holds the lock.
func prune(recs []recovery, now time.Time, window time.Duration) []recovery {
	cutoff := now.Add(-window)
	out := recs[:0]
	for _, r := range recs {
		if r.at.After(cutoff) {
			out = append(out, r)
		}
	}
	return out
}

// Gate decides whether the safety machine would ALLOW a recovery for a confirmed
// candidate right now, and returns the cap snapshot + the gating decision. It is
// pure w.r.t. recorded state (no side effects); RecordAttempt commits a would-be
// attempt. The returned Decision is DecisionAct when allowed, or the specific
// guard that blocked it (cap_hit / breaker_open).
//
// Ordering (most-protective first): breaker / flicker quarantine, then global
// fleet cap, then per-session cap. Backoff is folded into the per-session cap
// check via the most-recent attempt time.
func (p *PolicyMachine) Gate(c Candidate, now time.Time) (Decision, CapsState) {
	p.mu.Lock()
	defer p.mu.Unlock()

	sess := prune(p.perSession[c.SessionID], now, perSessionWindow)
	p.perSession[c.SessionID] = sess
	p.global = prune(p.global, now, globalWindow)

	state := CapsState{
		Session6h:    len(sess),
		GlobalHour:   len(p.global),
		BreakerFails: p.consecutiveFails[c.SessionID],
		BreakerOpen:  p.quarantined[c.SessionID] || p.flickering[c.SessionID],
	}

	// Circuit breaker / flicker quarantine: a session that flapped or already
	// failed K consecutive recoveries must not be acted on — escalate only.
	if state.BreakerOpen || p.consecutiveFails[c.SessionID] >= p.breakerLimit(c.Substate) {
		state.BreakerOpen = true
		return DecisionBreakerOpen, state
	}

	// Global fleet rate cap (TriageMaxPerHour precedent): a fleet-wide outage
	// can never become a fleet-wide restart storm.
	if len(p.global) >= p.caps.GlobalPerHour {
		return DecisionCapHit, state
	}

	// Per-session cap (folds in backoff: once the cap is hit, stop).
	if len(sess) >= p.perSessionLimit(c.Substate) {
		return DecisionCapHit, state
	}

	return DecisionAct, state
}

// RecordAttempt commits a would-be recovery attempt to the rolling windows. In
// observe mode this is what exercises the caps/backoff machine without any real
// action: the engine records the attempt it WOULD have made so the next cycle's
// Gate reflects it and the audit shows caps advancing.
func (p *PolicyMachine) RecordAttempt(c Candidate, now time.Time) {
	p.mu.Lock()
	defer p.mu.Unlock()
	rec := recovery{at: now, substate: c.Substate}
	p.perSession[c.SessionID] = append(prune(p.perSession[c.SessionID], now, perSessionWindow), rec)
	p.global = append(prune(p.global, now, globalWindow), rec)
}

// RecordOutcome updates the circuit breaker after a recovery's outcome is known.
// healthy=true resets the consecutive-fail counter; healthy=false increments it
// and, on reaching K, opens the breaker (quarantine). Used by Stages 2-3; in
// observe mode there is no real outcome, so it is exercised only in tests.
func (p *PolicyMachine) RecordOutcome(c Candidate, healthy bool) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if healthy {
		delete(p.consecutiveFails, c.SessionID)
		return
	}
	p.consecutiveFails[c.SessionID]++
	if p.consecutiveFails[c.SessionID] >= p.breakerLimit(c.Substate) {
		p.quarantined[c.SessionID] = true
	}
}

// IsQuarantined reports whether the session's breaker is open (manual clear or a
// return to healthy is required). flicker is treated as quarantine-equivalent.
func (p *PolicyMachine) IsQuarantined(sessionID string) bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.quarantined[sessionID] || p.flickering[sessionID]
}

// ClearQuarantine releases a session's breaker (human/Maestro action, §3.4).
func (p *PolicyMachine) ClearQuarantine(sessionID string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	delete(p.quarantined, sessionID)
	delete(p.consecutiveFails, sessionID)
}
