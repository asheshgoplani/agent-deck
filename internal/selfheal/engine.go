package selfheal

import (
	"errors"
	"sync"
	"time"

	"github.com/asheshgoplani/agent-deck/internal/tmux"
)

// ErrActionInGuardedMode is returned if an action executor is ever invoked while
// the engine is in a guarded mode (single_action / full). Stages 2-3 are HELD
// pending Ashesh re-approval + the three §9 gap-fixes, so those modes refuse to
// act. observe never reaches an executor at all.
var ErrActionInGuardedMode = errors.New("selfheal: actions are HELD (Stages 2-3 not re-approved)")

// ActionExecutor performs a real recovery. STAGE 1 NEVER CONSTRUCTS ONE — the
// observe-mode engine has a nil executor and a guarded mode refuses to call it.
// The interface exists so the wiring is in place for Stage 2 without reshaping
// the engine, but every method is unreachable in v1.9.67.
type ActionExecutor interface {
	// Execute applies the action to the candidate and reports the immediate
	// outcome string. Only ever called in single_action/full AFTER the §9 gaps
	// close and Ashesh re-approves.
	Execute(c Candidate, a Action) (outcome string, err error)
}

// Engine is the per-process self-heal policy. It owns the safety machine, the
// audit sink, and (for non-observe stages only) the action executor. In observe
// mode exec is nil and stage gating refuses any execution path, so "no recovery
// primitive is ever called" is structural.
type Engine struct {
	mode   Mode
	caps   Caps
	policy *PolicyMachine
	sink   EventSink
	// exec is nil in observe mode. Even when set, ProcessRead refuses to call it
	// unless the mode is single_action/full (guarded) — and those are HELD.
	exec ActionExecutor

	// mu guards the per-session bookkeeping maps below. Today ProcessRead is only
	// called from the single transition-daemon poll goroutine, but the lock makes
	// the engine safe if a future stage ever evaluates profiles in parallel
	// (defense-in-depth; the hot path is tiny so the lock is free).
	mu sync.Mutex

	// prevSig tracks the previous read's output signature per session, so the
	// engine can detect output movement (mid-turn) and require the two-read
	// confirm without the caller having to thread it.
	prevSig map[string]string
	// confirmed tracks the FIRST qualifying read per session (its signature +
	// the substate it was diagnosed as), so the current read can be the §1.3 #4
	// second confirming read AND we can require the diagnosis to MATCH across the
	// two reads (a model_unavailable read followed by an auth_401 read must NOT
	// confirm — they are different incidents).
	confirmed map[string]confirmState
	// substateSeen records, per session, when the engine FIRST observed the
	// current stuck substate (and which substate). This is the authoritative
	// dwell anchor: it is the moment the stuck condition began, reset the instant
	// the substate changes, the session goes healthy/busy/mid-turn, or output
	// moves. It fixes anchoring the dwell on a stale waiting timestamp and an
	// old send timestamp surviving a legitimate later idle.
	substateSeen map[string]substateEntry
}

// confirmState is the first qualifying read recorded for the two-read confirm.
type confirmState struct {
	read     ReadSig
	substate tmux.Substate
}

// substateEntry records when a stuck substate was first observed by the engine.
type substateEntry struct {
	substate tmux.Substate
	since    time.Time
}

// Config configures a new Engine.
type Config struct {
	Mode Mode
	Caps Caps
	Sink EventSink
}

// NewObserveEngine builds the Stage-1 observe-only engine. It has NO action
// executor — by construction it cannot take a recovery action. This is the only
// constructor the daemon uses in v1.9.67.
func NewObserveEngine(caps Caps, sink EventSink) *Engine {
	return &Engine{
		mode:      ModeObserve,
		caps:      caps,
		policy:    NewPolicyMachine(caps),
		sink:      sink,
		exec:         nil,
		prevSig:      map[string]string{},
		confirmed:    map[string]confirmState{},
		substateSeen: map[string]substateEntry{},
	}
}

// Policy exposes the safety machine (for the flicker subscription wiring).
func (e *Engine) Policy() *PolicyMachine { return e.policy }

// Mode returns the engine's current authority level.
func (e *Engine) Mode() Mode { return e.mode }

// ProcessRead evaluates ONE read of one candidate, advances the two-read confirm
// state-machine, exercises the safety machine, and emits exactly one audit event
// describing what self-heal would do. In observe mode it returns having taken
// NO action — guaranteed because:
//   - the executor is nil, and
//   - executeIfAuthorized refuses any non-observe mode (Stages 2-3 HELD).
//
// now must be a date-u-anchored monotonic-ish UTC time (the daemon anchors it).
// outputMoved/hookRunningFresh come from the caller's existing reads; the engine
// also folds in its own prevSig comparison so a caller that doesn't compute
// movement still gets it.
func (e *Engine) ProcessRead(c Candidate, now time.Time) Event {
	e.mu.Lock()
	defer e.mu.Unlock()
	// Subscribe-derived movement: if the caller didn't flag movement, derive it
	// from the prior signature this engine saw.
	if !c.OutputMoved {
		if prev, ok := e.prevSig[c.SessionID]; ok && prev != "" && c.OutputSig != "" && prev != c.OutputSig {
			c.OutputMoved = true
		}
	}
	e.prevSig[c.SessionID] = c.OutputSig

	// Track when the current STUCK substate began and use that as the dwell
	// anchor — it is the authoritative "the stuck condition started here" time,
	// independent of any stale waiting/send timestamp. The anchor resets the
	// instant the substate changes, the session leaves the stuck class, or output
	// moves. This is what stops (a) a months-old waiting timestamp from instantly
	// satisfying a freshly-observed model_unavailable dwell, and (b) an old
	// last_sent_at from re-flagging a session that legitimately went idle long
	// after its send. The caller's StatusChangedAt/LastSentAt are used only as a
	// floor so a real, persistent stuck condition is not under-counted.
	anchor := e.updateSubstateAnchor(c, now)
	if !anchor.IsZero() {
		c.StatusChangedAt = anchor
		// For idle_at_empty_prompt the dwell is measured from the LATER of the
		// send and the substate-entry: a session is only stuck if it has been at
		// the empty prompt (no output movement) since AND after we last sent it
		// something. If it produced output after the send, the anchor moved past
		// last_sent_at and the idle is fresh (not a stale send).
		if c.Substate == tmux.SubstateIdleAtEmptyPrompt && !c.LastSentAt.IsZero() && anchor.After(c.LastSentAt) {
			c.LastSentAt = anchor
		}
	}

	res := Evaluate(c, now)

	ev := Event{
		TS:        formatTS(now),
		SessionID: c.SessionID,
		Title:     c.Title,
		Group:     c.Group,
		Profile:   c.Profile,
		Account:   c.Account,
		Substate:  string(c.Substate),
		Dwell:     res.Dwell.Seconds(),
		Decision:  res.Decision,
		Stage:     e.mode,
	}

	if !res.Candidate {
		// Not a candidate this read → the confirm chain resets.
		delete(e.confirmed, c.SessionID)
		ev.Reads = []ReadSig{{T: formatTS(now), Sig: c.OutputSig}}
		_ = e.sink.Append(ev)
		return ev
	}

	// Candidate THIS read. Require a prior confirming read (§1.3 #4): one read is
	// never enough. The first qualifying read is recorded and we stand down; the
	// second confirming read is what proceeds to the gate — but ONLY if it is the
	// SAME diagnosis. A model_unavailable read followed by an auth_401 read is two
	// different incidents, not a confirmation: the second read replaces the first
	// and we re-confirm.
	first, hadFirst := e.confirmed[c.SessionID]
	thisRead := ReadSig{T: formatTS(now), Sig: c.OutputSig}
	if !hadFirst || first.substate != c.Substate {
		e.confirmed[c.SessionID] = confirmState{read: thisRead, substate: c.Substate}
		ev.Decision = DecisionSkipConfirm
		ev.Reads = []ReadSig{thisRead}
		_ = e.sink.Append(ev)
		return ev
	}

	// Two confirming reads of the SAME substate. Record both signatures.
	second := thisRead
	ev.Reads = []ReadSig{first.read, second}

	// Safety machine: caps / backoff / breaker / flicker.
	gate, capsState := e.policy.Gate(c, now)
	ev.Caps = capsState
	if gate != DecisionAct {
		// Blocked by a guard — escalate-only, take no recovery. Reset the confirm
		// chain so we re-confirm before the next would-be attempt.
		delete(e.confirmed, c.SessionID)
		ev.Decision = gate
		ev.WouldHave = ActionEscalate
		_ = e.sink.Append(ev)
		return ev
	}

	// Gate allowed: this is a confirmed, in-budget candidate. The safety machine
	// records the would-be attempt so caps/backoff advance and the next cycle
	// reflects it (Stage-1 brief item 4: the machine is exercised + logged).
	e.policy.RecordAttempt(c, now)
	delete(e.confirmed, c.SessionID)

	would := WouldHaveAction(c.Substate)
	ev.Decision = DecisionAct
	ev.WouldHave = would
	ev.ActionParams = actionParams(c.Substate)

	// OBSERVE: log would_have and STOP. No executor, no action. This is the whole
	// point of Stage 1.
	if e.mode == ModeObserve {
		ev.Outcome = "observe_noop"
		_ = e.sink.Append(ev)
		return ev
	}

	// Non-observe modes are HELD. The executor is refused even if one were set.
	outcome, action := e.executeIfAuthorized(c, would)
	ev.Action = action
	ev.Outcome = outcome
	_ = e.sink.Append(ev)
	return ev
}

// updateSubstateAnchor tracks when the current stuck substate was first observed
// for a session and returns that "since" time (zero when there is no stuck
// substate to anchor). The anchor RESETS whenever:
//   - the substate changes (a different incident starts its own clock),
//   - the session is busy / mid-turn (output moved) — it is actively working, not
//     stuck, so any prior stuck observation is void,
//   - the substate is not a stuck class.
//
// This is the engine's authoritative dwell anchor, independent of the caller's
// possibly-stale StatusChangedAt/LastSentAt.
func (e *Engine) updateSubstateAnchor(c Candidate, now time.Time) time.Time {
	// Any sign of life — or any disqualifying state — voids a stuck observation
	// outright. Stopped/OptedOut are included so a session cannot silently accrue
	// dwell while it is stopped or opted out and then instantly confirm the moment
	// it is reactivated / un-opted-out (Codex review). Output movement, busy, and
	// mid-turn are the active-work signals; a non-stuck substate has nothing to
	// anchor.
	if c.Busy || c.HookRunningFresh || c.OutputMoved || c.Stopped || c.OptedOut || !IsStuckSubstate(c.Substate) {
		delete(e.substateSeen, c.SessionID)
		return time.Time{}
	}
	prev, ok := e.substateSeen[c.SessionID]
	if !ok || prev.substate != c.Substate {
		// First observation of this stuck substate → start its clock now.
		e.substateSeen[c.SessionID] = substateEntry{substate: c.Substate, since: now}
		return now
	}
	return prev.since
}

// executeIfAuthorized is the single chokepoint where a real action could ever
// run. It REFUSES in every mode that ships in v1.9.67: observe (no executor) and
// the HELD single_action/full. Returns the outcome string and the action
// actually taken (ActionNone when refused).
func (e *Engine) executeIfAuthorized(c Candidate, would Action) (string, Action) {
	// Stages 2-3 are HELD pending re-approval + the three §9 gap-fixes. Until
	// then, even single_action/full refuse to act.
	if e.mode != ModeObserve { // both guarded modes land here
		return "held_stage_2_3", ActionNone
	}
	// Unreachable in observe (handled by the caller), but defensive: never act.
	if e.exec == nil {
		return "no_executor", ActionNone
	}
	outcome, err := e.exec.Execute(c, would)
	if err != nil {
		return "error:" + err.Error(), ActionNone
	}
	return outcome, would
}

// actionParams records the would-be action's parameters for the audit (§5).
func actionParams(s tmux.Substate) map[string]any {
	switch s {
	case tmux.SubstateModelUnavailable:
		return map[string]any{"model": "opus", "reissue": true}
	case tmux.SubstateAuth401:
		return map[string]any{"reassert_creds": true}
	default:
		return nil
	}
}
