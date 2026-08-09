package selfheal

import (
	"errors"
	"sync"
	"time"

	"github.com/asheshgoplani/agent-deck/internal/tmux"
)

// ErrActionInGuardedMode is returned by the execution chokepoint for every
// (mode, action) pair except (ModeResume, ActionResume). single_action / full
// are HELD pending re-approval + the three §9 gap-fixes; observe never reaches
// the chokepoint at all.
var ErrActionInGuardedMode = errors.New("selfheal: actions are HELD (Stages 2-3 not re-approved)")

// ErrNoExecutor is returned when an ACTING engine reaches the chokepoint holding
// no executor. NewResumeEngine requires one, so this is a mis-wire — and it is
// reported as an error rather than a nil-error no-op precisely so it cannot read
// as a normal quiet decline to a caller inspecting the error.
//
// Be clear about its OBSERVABLE effect today: executeIfAuthorized is called from
// exactly one place, which discards the error, so the only thing this value
// changes for an operator is the "no_executor" outcome string in the audit
// record. It exists so a future caller that DOES inspect the error can tell a
// mis-wire from a refusal.
var ErrNoExecutor = errors.New("selfheal: acting engine has no executor")

// ActionExecutor performs a real recovery. It is reachable ONLY through
// Engine.executeIfAuthorized, and only for the pair (ModeResume, ActionResume) —
// observe holds a nil executor and every other mode/action pair is refused with
// ErrActionInGuardedMode.
type ActionExecutor interface {
	// Execute applies the action to the candidate and reports the immediate
	// outcome string. The outcome MUST carry the real delivery verdict (the
	// caller records it verbatim in the audit event), so a resume that was typed
	// but never submitted is visible as a failure rather than a success.
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
	// draftMemo caches protective "draft present" answers per session for
	// composerDraftTTL. Empty answers are never cached. See hasComposerDraft.
	draftMemo map[string]draftEntry
}

// composerDraftTTL bounds how long a protective "draft present" answer delays
// another composer capture.
//
// It has to sit between two costs. Below it, a drafted wedged session that keeps
// clearing the two-read confirm forever re-forks `tmux capture-pane` on every
// act-eligible read, roughly every other poll, unbounded in time. Above it, a
// cleared draft remains conservatively held for longer than necessary. Empty
// answers are not memoised, so a newly-typed draft is visible on the next
// deciding read regardless of this TTL.
//
// 10s: the shortest dwell threshold in the policy is 60s, so this is one sixth
// of the window a session must sit stuck before anything can happen to it, and
// it is at least three polls at the fastest 1s cadence. Against the measured
// 30-minute wedged tail (900 reads at a 2s poll, 435 act-eligible reads, 435
// lookups) it bounds the lookups at ~180 — the tail stops scaling with the poll
// rate and starts scaling with wall-clock, which is the property that was
// missing.
const composerDraftTTL = 10 * time.Second

// draftEntry records when a memoised "draft present" answer was resolved.
type draftEntry struct {
	at time.Time
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
		mode:         ModeObserve,
		caps:         caps,
		policy:       NewPolicyMachine(caps),
		sink:         sink,
		exec:         nil,
		prevSig:      map[string]string{},
		confirmed:    map[string]confirmState{},
		substateSeen: map[string]substateEntry{},
		draftMemo:    map[string]draftEntry{},
	}
}

// NewResumeEngine builds the ONE acting engine: mode "resume", authorised for
// exactly the pair (ModeResume, ActionResume) and nothing else. Every other
// action — including ActionResend and the two restart actions — still refuses at
// the chokepoint, so approving this mode approves one narrow path rather than
// reopening single_action / full.
//
// exec must be non-nil; an acting engine with no executor is a mis-wire, and the
// chokepoint reports it as such rather than silently doing nothing.
func NewResumeEngine(caps Caps, sink EventSink, exec ActionExecutor) *Engine {
	return &Engine{
		mode:         ModeResume,
		caps:         caps,
		policy:       NewPolicyMachine(caps),
		sink:         sink,
		exec:         exec,
		prevSig:      map[string]string{},
		confirmed:    map[string]confirmState{},
		substateSeen: map[string]substateEntry{},
		draftMemo:    map[string]draftEntry{},
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
		// Not a candidate this read → the confirm chain resets. The session has
		// demonstrably left the stuck class, so drop its composer-draft memo too:
		// the engine is long-lived per profile in a daemon that runs for weeks, and
		// without this every session it has ever evaluated leaves a permanent
		// entry. Evicting here also makes the next answer fresher rather than
		// staler.
		delete(e.confirmed, c.SessionID)
		delete(e.draftMemo, c.SessionID)
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

	would := WouldHaveAction(c.Substate)

	// D6: an operator draft in the composer is a HARD precondition, evaluated
	// BEFORE the safety machine on purpose — see the note above this code block.
	// A draft is not a failed recovery and must not spend one.
	//
	// Submitting someone else's text is not a decision a status probe gets to
	// make, and the force-send path is known to CONSUME the draft rather than
	// restore it (2026-08-07, conductor2-testfix). Downgrading to escalate here
	// means the audit records a session that needs a human, no cap is spent, and
	// no mode — including observe — can proceed past it. Observe short-circuits
	// on the same branch deliberately: observe exists to model what resume WOULD
	// do, and resume spends nothing here.
	//
	// This is the ONLY place the deferred draft lookup is consulted. Every earlier
	// exit — not-a-candidate, skip_dwell, skip_confirm — returns without touching
	// it, so the reads that can only decline cost no pane capture at all.
	//
	// It is NOT once per wedged session. This branch sits above the safety
	// machine, so every read that clears the two-read confirm reaches it — and a
	// session whose breaker is open or whose 2-per-6h cap is spent keeps clearing
	// that confirm indefinitely, at roughly every other poll, for as long as it
	// stays wedged. hasComposerDraft is what bounds the resulting capture rate:
	// the branch still asks on every such read, it just does not re-fork if it
	// already asked within composerDraftTTL.
	//
	// The gate is narrowed to ActionResume because ActionResume is the only
	// action this PR authorises, NOT because a draft is safe to type over for the
	// others: D6's rationale — submitting someone else's text is not a decision a
	// status probe gets to make — is not action-specific. ActionResend refuses at
	// the chokepoint today, which is the only reason idle_at_empty_prompt is not
	// a live hole. Whoever authorises ActionResend next must widen this gate in
	// the same change.
	if would == ActionResume && e.hasComposerDraft(c, now) {
		delete(e.confirmed, c.SessionID)
		ev.Decision = DecisionAct
		ev.WouldHave = ActionEscalate
		ev.Action = ActionNone
		ev.ActionParams = actionParams(c.Substate)
		// Its OWN outcome string. "held_stage_2_3" means "Stages 2-3 are held",
		// which is a different fact entirely; an operator grepping the audit for
		// why a session was never resumed has to be able to see that a human's
		// draft is what stopped it.
		ev.Outcome = "held_composer_draft"
		_ = e.sink.Append(ev)
		return ev
	}

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

	// Every non-observe mode goes through the chokepoint. Only (resume, resume)
	// is authorised; single_action and full stay HELD.
	outcome, action, _ := e.executeIfAuthorized(c, would)
	ev.Action = action
	ev.Outcome = outcome
	// Feed the circuit breaker only when a real action ran. A refusal is not a
	// failed recovery — counting it would open the breaker on sessions self-heal
	// never touched.
	//
	// Note what this deliberately makes invisible: an executor ERROR also returns
	// ActionNone, so it never reaches the breaker either, even though
	// RecordAttempt above has already spent one of the session's two 6-hour
	// recoveries. A session that errors every attempt goes quiet after two, via
	// cap_hit rather than breaker_open. The per-session cap is what bounds the
	// damage, and docs/self-heal.md tells operators to read the error text
	// because the breaker will not. Whether the breaker SHOULD see executor
	// errors is an open design question, not an oversight here.
	//
	// The success argument is outcomeIsDelivered alone, deliberately: every
	// executeIfAuthorized path that returns a non-ActionNone action returns a nil
	// error, so conjoining err == nil here would read as error handling the
	// comment above has just said is not happening.
	if action != ActionNone {
		e.policy.RecordOutcome(c, outcomeIsDelivered(outcome))
	}
	_ = e.sink.Append(ev)
	return ev
}

// outcomeResumePrefix is the executor's outcome namespace: a resume reports
// "resumed:<delivery>", where delivery is the `session send` contract value.
//
// ResumeOutcome is the ONE producer of that string, and outcomeIsDelivered the
// one matcher. They live together because the executor is in another package
// (internal/session, which cannot be imported from here) and the two halves used
// to be joined only by a sentence in the task spec — a drift in either would
// silently reclassify every recovery as a failure, or worse, the reverse.
const outcomeResumePrefix = "resumed:"

// outcomeDelivered is the ONLY outcome that means the agent actually took the
// message up. It is an exact value, not a prefix: "typed_not_submitted" in
// particular means the bytes are sitting in a composer that is not accepting
// Enter, which is a failure, and a prefix match on "resumed:submitted" would
// also swallow a future "resumed:submitted_late".
const outcomeDelivered = outcomeResumePrefix + "submitted"

// ResumeOutcome formats an ActionResume executor's outcome from the `session
// send` delivery verdict. Executors must build their outcome through this rather
// than concatenating the prefix themselves.
func ResumeOutcome(delivery string) string { return outcomeResumePrefix + delivery }

// outcomeIsDelivered reports whether an executor outcome represents a genuinely
// accepted turn, for the circuit breaker's consecutive-failure count.
func outcomeIsDelivered(outcome string) bool {
	return outcome == outcomeDelivered
}

// hasComposerDraft answers "does the target's composer hold operator text".
// Protective true answers are memoised for composerDraftTTL; false answers are
// permission for the current deciding read only and are resolved again on the
// next deciding read. Callers must hold e.mu.
//
// A nil lookup means the caller has no way to look, which reads as "no draft"
// and is never memoised — there is nothing to memoise and no subprocess to save.
//
// A capture that FAILS never arrives here as "no draft": the lookup owns that
// fail-safe and reports a failed capture as "there might be a draft"
// (candidate.go), so failures receive the same protective true memo. The memo
// can delay a resume by up to the TTL after an operator clears their draft, but
// it can never let a newly-typed draft hide behind a stale empty answer.
func (e *Engine) hasComposerDraft(c Candidate, now time.Time) bool {
	if c.ComposerDraft == nil {
		return false
	}
	if memo, ok := e.draftMemo[c.SessionID]; ok {
		if age := now.Sub(memo.at); age >= 0 && age < composerDraftTTL {
			return true
		}
	}
	draft := c.ComposerDraft()
	if draft {
		e.draftMemo[c.SessionID] = draftEntry{at: now}
	} else {
		delete(e.draftMemo, c.SessionID)
	}
	return draft
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

// executeIfAuthorized is the single chokepoint where a real action can run. It
// authorises EXACTLY ONE pair — (ModeResume, ActionResume) — and refuses
// everything else with ErrActionInGuardedMode: observe never gets here, and
// single_action / full stay HELD pending re-approval + the three §9 gap-fixes.
//
// The pair check is deliberately a conjunction rather than two nested guards: a
// future mode that forgets to narrow its action set, or a substate that starts
// mapping to a restart, both land on the refusal instead of on a live restart.
//
// Returns the outcome string, the action actually taken (ActionNone when
// refused), and the refusal/execution error.
func (e *Engine) executeIfAuthorized(c Candidate, would Action) (string, Action, error) {
	if e.mode != ModeResume || would != ActionResume {
		// Byte-identical to the pre-resume outcome string so anything reading the
		// audit history for held records keeps matching.
		return "held_stage_2_3", ActionNone, ErrActionInGuardedMode
	}
	if e.exec == nil {
		// An acting engine with no executor is a mis-wire, not a quiet no-op — so
		// it returns an error, matching what this comment has always claimed.
		return "no_executor", ActionNone, ErrNoExecutor
	}
	outcome, err := e.exec.Execute(c, would)
	if err != nil {
		return "error:" + err.Error(), ActionNone, err
	}
	return outcome, would, nil
}

// actionParams records the would-be action's parameters for the audit (§5).
func actionParams(s tmux.Substate) map[string]any {
	switch s {
	case tmux.SubstateModelUnavailable:
		return map[string]any{"model": "opus", "reissue": true}
	case tmux.SubstateAuth401:
		return map[string]any{"reassert_creds": true}
	case tmux.SubstateAPIError:
		// The reason selects the executor's prompt text. A transport resume just
		// asks the turn to continue.
		return map[string]any{"reason": "transport"}
	case tmux.SubstateUsageLimit:
		// The usage_limit prompt additionally warns that a SUBAGENT may have been
		// terminated by the limit and needs re-dispatching — resuming the parent
		// does not restore a child's work.
		return map[string]any{"reason": "usage_limit"}
	default:
		return nil
	}
}
