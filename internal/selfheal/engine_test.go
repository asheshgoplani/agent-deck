package selfheal

import (
	"errors"
	"testing"
	"time"

	"github.com/asheshgoplani/agent-deck/internal/tmux"
)

// spyExecutor records every Execute call. In observe mode it must NEVER be
// called — that is the load-bearing proof of the observe-only invariant.
type spyExecutor struct {
	calls []Action
}

func (s *spyExecutor) Execute(c Candidate, a Action) (string, error) {
	s.calls = append(s.calls, a)
	return "executed", nil
}

func dwelledModelCand(now time.Time) Candidate {
	return Candidate{
		SessionID: "s1-1780000000",
		Title:     "exec-fix",
		Profile:   "personal",
		Substate:  tmux.SubstateModelUnavailable,
		OutputSig: "sigA", // stable across reads → no movement
	}
}

// driveToAct feeds the same candidate at a fixed cadence (default 1 min apart)
// up to a bound and returns the first "act" event plus how many reads it took.
// The engine anchors dwell on the FIRST observation of the stuck substate, so a
// candidate becomes actionable only after it has been observed across enough
// polls to (a) pass the dwell threshold AND (b) confirm over two same-substate
// reads — exactly the conservative behavior we want.
func driveToAct(t *testing.T, e *Engine, c Candidate, start time.Time, step time.Duration, max int) (Event, int) {
	t.Helper()
	now := start
	for i := 1; i <= max; i++ {
		ev := e.ProcessRead(c, now)
		if ev.Action != ActionNone {
			t.Fatalf("read %d emitted an action %q (observe must never act)", i, ev.Action)
		}
		if ev.Decision == DecisionAct {
			return ev, i
		}
		now = now.Add(step)
	}
	t.Fatalf("candidate never reached 'act' within %d reads", max)
	return Event{}, 0
}

// The headline guarantee: in observe mode, a fully-confirmed stuck candidate
// eventually emits an "act" decision with would_have set, observe_noop outcome,
// NO action field; and the engine holds NO executor.
func TestObserve_ConfirmedCandidate_LogsWouldHave_NoAction(t *testing.T) {
	now := time.Unix(1780000000, 0).UTC()
	sink := &MemorySink{}
	e := NewObserveEngine(DefaultCaps(), sink)

	if e.exec != nil {
		t.Fatal("observe engine must hold NO action executor")
	}

	// First read starts the dwell clock (anchor = now), so it cannot be a
	// candidate yet — proving we never fire on a single observation.
	ev1 := e.ProcessRead(dwelledModelCand(now), now)
	if ev1.Decision == DecisionAct {
		t.Fatalf("first observation must never act, got %s", ev1.Decision)
	}

	act, reads := driveToAct(t, e, dwelledModelCand(now), now, time.Minute, 10)
	if reads < 3 {
		t.Fatalf("model_unavailable should need >=3 reads (dwell 90s + 2-read confirm), took %d", reads)
	}
	if act.WouldHave != ActionRestartModelSwitch {
		t.Fatalf("would_have: want restart_model_switch, got %q", act.WouldHave)
	}
	if act.Action != ActionNone {
		t.Fatalf("OBSERVE MUST TAKE NO ACTION: action field = %q", act.Action)
	}
	if act.Outcome != "observe_noop" {
		t.Fatalf("outcome: want observe_noop, got %q", act.Outcome)
	}
	if act.Stage != ModeObserve {
		t.Fatalf("stage must be observe, got %q", act.Stage)
	}
	if len(act.Reads) != 2 {
		t.Fatalf("act event must record both confirming reads, got %d", len(act.Reads))
	}
}

// First observation of a freshly-stuck substate never fires, even if the
// caller's StatusChangedAt is ancient — the engine anchors dwell on when IT
// first saw the substate (Codex finding #4: no instant-fire on a stale anchor).
func TestObserve_FreshSubstate_StaleAnchor_NoInstantFire(t *testing.T) {
	now := time.Unix(1780000000, 0).UTC()
	e := NewObserveEngine(DefaultCaps(), &MemorySink{})
	c := dwelledModelCand(now)
	c.StatusChangedAt = now.Add(-30 * 24 * time.Hour) // month-old waiting ts
	ev := e.ProcessRead(c, now)
	if ev.Decision == DecisionAct {
		t.Fatal("a freshly-observed stuck substate with a stale anchor must NOT instantly fire")
	}
	if ev.Dwell > 1 {
		t.Fatalf("dwell must be measured from first observation (~0), got %.0fs", ev.Dwell)
	}
}

// Codex finding #2: two reads of DIFFERENT substates must not confirm. A
// model_unavailable read followed by an auth_401 read is two incidents.
func TestObserve_ConfirmRequiresSameSubstate(t *testing.T) {
	now := time.Unix(1780000000, 0).UTC()
	e := NewObserveEngine(DefaultCaps(), &MemorySink{})
	sid := "s1"

	// Drive model_unavailable past its dwell so it would be a candidate...
	mk := func(sub tmux.Substate) Candidate {
		return Candidate{SessionID: sid, Substate: sub, OutputSig: "x"}
	}
	_ = e.ProcessRead(mk(tmux.SubstateModelUnavailable), now)                    // anchor
	_ = e.ProcessRead(mk(tmux.SubstateModelUnavailable), now.Add(2*time.Minute)) // dwelled → first confirm
	// Now the substate FLIPS to auth_401 on the would-be confirming read. Because
	// auth_401's anchor just started, it is not dwelled AND the diagnosis differs,
	// so it must NOT act.
	ev := e.ProcessRead(mk(tmux.SubstateAuth401), now.Add(3*time.Minute))
	if ev.Decision == DecisionAct {
		t.Fatalf("a substate change must reset the confirm, got act")
	}
}

// Even if a candidate confirms many times, observe mode never calls any executor.
// We can't inject one into NewObserveEngine (by design), so we assert the
// chokepoint refuses in observe and that processing emits no action over a long
// run including a cap-hit.
func TestObserve_NeverCallsExecutor_OverManyCycles(t *testing.T) {
	now := time.Unix(1780000000, 0).UTC()
	sink := &MemorySink{}
	e := NewObserveEngine(DefaultCaps(), sink)
	c := dwelledModelCand(now)

	for i := 0; i < 20; i++ {
		ev := e.ProcessRead(c, now)
		if ev.Action != ActionNone {
			t.Fatalf("cycle %d: observe emitted an action %q", i, ev.Action)
		}
		now = now.Add(2 * time.Minute)
	}
	// Some events must have been "act" (would_have), and at least one cap_hit
	// after the per-session cap (2) was exhausted — proving the safety machine
	// was exercised AND logged, all while taking no action.
	var sawAct, sawCap bool
	for _, ev := range sink.Snapshot() {
		if ev.Decision == DecisionAct {
			sawAct = true
		}
		if ev.Decision == DecisionCapHit {
			sawCap = true
		}
		if ev.Action != ActionNone {
			t.Fatalf("audit shows an action taken in observe: %q", ev.Action)
		}
	}
	if !sawAct {
		t.Fatal("expected at least one would-act event")
	}
	if !sawCap {
		t.Fatal("expected the per-session cap to fire and be logged (machine exercised)")
	}
}

// The chokepoint refuses even when a guarded mode somehow has an executor: Stages
// 2-3 are HELD. This guards against a future mis-wire shipping actions early.
func TestExecuteIfAuthorized_GuardedModes_Refuse(t *testing.T) {
	c := Candidate{SessionID: "s1", Substate: tmux.SubstateModelUnavailable}
	for _, m := range []Mode{ModeSingleAction, ModeFull} {
		spy := &spyExecutor{}
		e := &Engine{mode: m, caps: DefaultCaps(), policy: NewPolicyMachine(DefaultCaps()), sink: &MemorySink{}, exec: spy, prevSig: map[string]string{}, confirmed: map[string]confirmState{}, substateSeen: map[string]substateEntry{}, draftMemo: map[string]draftEntry{}}
		outcome, action, err := e.executeIfAuthorized(c, ActionRestartModelSwitch)
		if action != ActionNone {
			t.Fatalf("mode %q: HELD modes must take no action, got %q", m, action)
		}
		if outcome != "held_stage_2_3" {
			t.Fatalf("mode %q: want held_stage_2_3 outcome, got %q", m, outcome)
		}
		if !errors.Is(err, ErrActionInGuardedMode) {
			t.Fatalf("mode %q: want ErrActionInGuardedMode, got %v", m, err)
		}
		if len(spy.calls) != 0 {
			t.Fatalf("mode %q: executor must NOT be called (Stages 2-3 HELD), got %d calls", m, len(spy.calls))
		}
	}
}

// A busy session over two reads never reaches act, even confirmed-looking.
func TestObserve_BusySession_NeverActs(t *testing.T) {
	now := time.Unix(1780000000, 0).UTC()
	sink := &MemorySink{}
	e := NewObserveEngine(DefaultCaps(), sink)
	c := dwelledModelCand(now)
	c.Busy = true
	for i := 0; i < 5; i++ {
		ev := e.ProcessRead(c, now.Add(time.Duration(i)*time.Minute))
		if ev.Decision != DecisionSkipBusy {
			t.Fatalf("busy session must skip_busy, got %s", ev.Decision)
		}
		if ev.WouldHave != ActionNone {
			t.Fatalf("busy session must have no would_have, got %q", ev.WouldHave)
		}
	}
}

// The two-read drop: once dwelled and on its first confirming read, if the
// session then shows output movement (mid-turn), it must NOT act (§1.3 #4).
func TestObserve_TwoReadDrop_OnMovement(t *testing.T) {
	now := time.Unix(1780000000, 0).UTC()
	sink := &MemorySink{}
	e := NewObserveEngine(DefaultCaps(), sink)

	c := dwelledModelCand(now)
	c.OutputSig = "sigA"
	_ = e.ProcessRead(c, now)                       // anchor (dwell 0)
	ev2 := e.ProcessRead(c, now.Add(2*time.Minute)) // dwelled → first confirm
	if ev2.Decision != DecisionSkipConfirm {
		t.Fatalf("read2 (dwelled, first candidate): want skip_confirm, got %s", ev2.Decision)
	}
	// read3: output moved → mid-turn → not a candidate, confirm chain resets.
	c3 := c
	c3.OutputSig = "sigB" // movement detected by engine's prevSig diff
	ev3 := e.ProcessRead(c3, now.Add(4*time.Minute))
	if ev3.Decision != DecisionSkipMidTurn {
		t.Fatalf("read3 with movement: want skip_midturn, got %s", ev3.Decision)
	}
	if ev3.WouldHave != ActionNone {
		t.Fatalf("dropped read must not set would_have, got %q", ev3.WouldHave)
	}
}

// Each substate yields the correct would_have over the confirm flow.
func TestObserve_PerSubstate_WouldHave(t *testing.T) {
	cases := []struct {
		sub  tmux.Substate
		want Action
		mk   func() Candidate
	}{
		{tmux.SubstateModelUnavailable, ActionRestartModelSwitch, func() Candidate {
			return Candidate{SessionID: "m", Substate: tmux.SubstateModelUnavailable, OutputSig: "x"}
		}},
		{tmux.SubstateAuth401, ActionRestartReassertCreds, func() Candidate {
			return Candidate{SessionID: "a", Substate: tmux.SubstateAuth401, OutputSig: "x"}
		}},
		{tmux.SubstateIdleAtEmptyPrompt, ActionResend, func() Candidate {
			// idle dwell is from last_sent_at; the engine anchors substate-entry at
			// first observation, so a recent send + accruing reads reach act.
			return Candidate{SessionID: "i", Substate: tmux.SubstateIdleAtEmptyPrompt, LastSentAt: time.Unix(1780000000, 0).Add(-1 * time.Minute), OutputSig: "x"}
		}},
	}
	for _, tc := range cases {
		now := time.Unix(1780000000, 0).UTC()
		e := NewObserveEngine(DefaultCaps(), &MemorySink{})
		ev, _ := driveToAct(t, e, tc.mk(), now, time.Minute, 12)
		if ev.WouldHave != tc.want {
			t.Fatalf("%s: would_have = %q, want %q", tc.sub, ev.WouldHave, tc.want)
		}
		if ev.Action != ActionNone {
			t.Fatalf("%s: observe took an action %q", tc.sub, ev.Action)
		}
	}
}

// A stopped (or opted-out) session must not accrue dwell while disqualified and
// then instantly confirm on reactivation — the anchor resets while stopped.
func TestObserve_StoppedAccruesNoDwell(t *testing.T) {
	now := time.Unix(1780000000, 0).UTC()
	e := NewObserveEngine(DefaultCaps(), &MemorySink{})
	sid := "s1"
	stuck := func(stopped bool) Candidate {
		return Candidate{SessionID: sid, Substate: tmux.SubstateModelUnavailable, Stopped: stopped, OutputSig: "x"}
	}
	// Many reads while STOPPED — must accrue no dwell.
	for i := 0; i < 10; i++ {
		ev := e.ProcessRead(stuck(true), now.Add(time.Duration(i)*time.Minute))
		if ev.Decision == DecisionAct || ev.Decision == DecisionSkipConfirm {
			t.Fatalf("stopped session must never accrue toward act, got %s", ev.Decision)
		}
	}
	// Reactivated: the FIRST live read starts the clock fresh (dwell ~0), so it
	// cannot instantly confirm.
	ev := e.ProcessRead(stuck(false), now.Add(20*time.Minute))
	if ev.Decision == DecisionAct {
		t.Fatal("a just-reactivated session must not instantly fire on dwell accrued while stopped")
	}
}

// Codex finding #1: a session that got a send, produced output (moving past the
// send), and only LATER went idle must NOT be flagged off the stale send. The
// output movement resets the substate anchor, so the idle dwell restarts from
// the fresh idle, not the old send.
func TestObserve_IdleAfterOutputMoved_NotStaleSendCandidate(t *testing.T) {
	now := time.Unix(1780000000, 0).UTC()
	e := NewObserveEngine(DefaultCaps(), &MemorySink{})
	sid := "i1"
	oldSend := now.Add(-30 * time.Minute) // a long-ago send

	// The session produced output (still working) AFTER the send — output moved.
	working := Candidate{SessionID: sid, Substate: tmux.SubstateIdleAtEmptyPrompt, LastSentAt: oldSend, OutputSig: "a", OutputMoved: true}
	_ = e.ProcessRead(working, now)

	// Now it goes idle. The idle clock must start from HERE, not the 30-min-old
	// send — so a single fresh idle read is not instantly a >5-min-stale candidate.
	idle := Candidate{SessionID: sid, Substate: tmux.SubstateIdleAtEmptyPrompt, LastSentAt: oldSend, OutputSig: "a"}
	ev := e.ProcessRead(idle, now.Add(1*time.Second))
	if ev.Decision == DecisionAct {
		t.Fatal("a freshly-idle session must not fire off a 30-min-old send (anchor reset on output movement)")
	}
}

// The three reads an api-error candidate needs, given task 02's 60s dwell.
const (
	apiReadAnchor   = 0 * time.Second  // starts the dwell clock (skip_dwell)
	apiReadConfirm1 = 61 * time.Second // first read past the dwell (skip_confirm)
	apiReadConfirm2 = 63 * time.Second // second confirming read (act)
)

// resumeSpy records what the executor was asked to do and returns a canned
// outcome, so the chokepoint can be tested without any tmux or send path.
type resumeSpy struct {
	calls   []Action
	outcome string
	err     error
}

func (s *resumeSpy) Execute(c Candidate, a Action) (string, error) {
	s.calls = append(s.calls, a)
	return s.outcome, s.err
}

func resumeEngineFor(exec ActionExecutor, sink EventSink) *Engine {
	return NewResumeEngine(DefaultCaps(), sink, exec)
}

// apiErrorCand is a session showing the transport banner, with a stable output
// signature so nothing reads as movement.
//
// StatusChangedAt is `now`, NOT a pre-dwelled past time: ProcessRead overwrites
// it with its own anchor (see the note above), so pre-dwelling the field does
// nothing and only makes the test read as if the dwell were already satisfied.
// Dwell comes from the read timings, never from this struct.
func apiErrorCand(now time.Time) Candidate {
	return Candidate{
		SessionID:       "s1",
		Title:           "worker-3",
		Substate:        tmux.SubstateAPIError,
		StatusChangedAt: now,
		OutputSig:       "sig-stable",
	}
}

// §6: (ModeResume, ActionResume) executes.
func TestExecuteIfAuthorized_ResumePair_Executes(t *testing.T) {
	spy := &resumeSpy{outcome: "resumed:submitted"}
	e := resumeEngineFor(spy, &MemorySink{})
	outcome, action, err := e.executeIfAuthorized(Candidate{SessionID: "s1", Substate: tmux.SubstateAPIError}, ActionResume)
	if err != nil {
		t.Fatalf("the authorised pair must not error, got %v", err)
	}
	if action != ActionResume {
		t.Fatalf("want %q, got %q", ActionResume, action)
	}
	if outcome != "resumed:submitted" {
		t.Fatalf("outcome must be the executor's verbatim, got %q", outcome)
	}
	if len(spy.calls) != 1 {
		t.Fatalf("executor must be called exactly once, got %d", len(spy.calls))
	}
}

// §6: every other (mode, action) pair returns ErrActionInGuardedMode. This is the
// exhaustive matrix — the whole point of the chokepoint.
func TestExecuteIfAuthorized_EveryOtherPair_Refuses(t *testing.T) {
	modes := []Mode{ModeObserve, ModeSingleAction, ModeFull, ModeResume}
	actions := []Action{ActionRestartModelSwitch, ActionRestart, ActionResend, ActionRestartReassertCreds, ActionEscalate, ActionResume, ActionNone}
	for _, m := range modes {
		for _, a := range actions {
			if m == ModeResume && a == ActionResume {
				continue // the one authorised pair
			}
			spy := &resumeSpy{outcome: "resumed:submitted"}
			e := &Engine{mode: m, caps: DefaultCaps(), policy: NewPolicyMachine(DefaultCaps()), sink: &MemorySink{}, exec: spy, prevSig: map[string]string{}, confirmed: map[string]confirmState{}, substateSeen: map[string]substateEntry{}, draftMemo: map[string]draftEntry{}}
			_, action, err := e.executeIfAuthorized(Candidate{SessionID: "s1"}, a)
			if !errors.Is(err, ErrActionInGuardedMode) {
				t.Fatalf("(%q, %q): want ErrActionInGuardedMode, got %v", m, a, err)
			}
			if action != ActionNone {
				t.Fatalf("(%q, %q): must take no action, got %q", m, a, action)
			}
			if len(spy.calls) != 0 {
				t.Fatalf("(%q, %q): executor must not be called", m, a)
			}
		}
	}
}

// An acting engine holding no executor is a mis-wire. It must report one: a nil
// error there reads as an ordinary quiet decline to anything inspecting it.
func TestExecuteIfAuthorized_ActingEngineWithNoExecutor_IsAnError(t *testing.T) {
	e := &Engine{mode: ModeResume, caps: DefaultCaps(), policy: NewPolicyMachine(DefaultCaps()), sink: &MemorySink{}, exec: nil, prevSig: map[string]string{}, confirmed: map[string]confirmState{}, substateSeen: map[string]substateEntry{}, draftMemo: map[string]draftEntry{}}
	outcome, action, err := e.executeIfAuthorized(Candidate{SessionID: "s1"}, ActionResume)
	if !errors.Is(err, ErrNoExecutor) {
		t.Fatalf("want ErrNoExecutor, got %v", err)
	}
	if action != ActionNone {
		t.Fatalf("a mis-wire takes no action, got %q", action)
	}
	if outcome != "no_executor" {
		t.Fatalf("outcome = %q, want no_executor", outcome)
	}
}

// The executor builds its outcome through ResumeOutcome and the engine matches it
// here. The two live in different packages, so the round-trip is the only thing
// stopping a drift from silently reclassifying every recovery.
func TestResumeOutcome_RoundTripsThroughOutcomeIsDelivered(t *testing.T) {
	if got := ResumeOutcome("submitted"); got != "resumed:submitted" {
		t.Fatalf("ResumeOutcome(submitted) = %q", got)
	}
	if !outcomeIsDelivered(ResumeOutcome("submitted")) {
		t.Fatal("a submitted delivery is the healthy outcome")
	}
	for _, d := range []string{"typed", "typed_not_submitted", "unverified", "no_evidence", "line_too_long", "send_failed", "submitted_late"} {
		if outcomeIsDelivered(ResumeOutcome(d)) {
			t.Fatalf("%q must count as a FAILED recovery — only an accepted turn resets the breaker", d)
		}
	}
}

// §6: ModeObserve still constructs no executor.
func TestNewObserveEngine_StillHasNoExecutor(t *testing.T) {
	e := NewObserveEngine(DefaultCaps(), &MemorySink{})
	if e.exec != nil {
		t.Fatal("the observe engine must hold no executor — that is the structural guarantee")
	}
	if e.Mode() != ModeObserve {
		t.Fatalf("want %q, got %q", ModeObserve, e.Mode())
	}
}

// End-to-end through ProcessRead: anchor, dwell, two confirming reads, then
// exactly one execution.
func TestProcessRead_ResumeMode_ExecutesOncePerConfirmedCandidate(t *testing.T) {
	now := time.Unix(1780000000, 0).UTC()
	spy := &resumeSpy{outcome: "resumed:submitted"}
	sink := &MemorySink{}
	e := resumeEngineFor(spy, sink)
	c := apiErrorCand(now)

	anchor := e.ProcessRead(c, now.Add(apiReadAnchor))
	if anchor.Decision != DecisionSkipDwell {
		t.Fatalf("the first read only starts the dwell clock, got %q", anchor.Decision)
	}

	first := e.ProcessRead(c, now.Add(apiReadConfirm1))
	if first.Decision != DecisionSkipConfirm {
		t.Fatalf("the first dwelled read must only record the confirm, got %q", first.Decision)
	}
	if len(spy.calls) != 0 {
		t.Fatal("one qualifying read is never enough — no execution before the confirm")
	}

	second := e.ProcessRead(c, now.Add(apiReadConfirm2))
	if second.Decision != DecisionAct {
		t.Fatalf("the second confirming read must act, got %q", second.Decision)
	}
	if second.Action != ActionResume {
		t.Fatalf("want action %q, got %q", ActionResume, second.Action)
	}
	if second.Outcome != "resumed:submitted" {
		t.Fatalf("the audit must carry the real delivery, got %q", second.Outcome)
	}
	if got := second.ActionParams["reason"]; got != "transport" {
		t.Fatalf("api-error params reason: want transport, got %v", got)
	}
	if len(spy.calls) != 1 {
		t.Fatalf("exactly one delivery per confirmed candidate, got %d", len(spy.calls))
	}
}

// draftPresent / draftAbsent are the deferred-lookup forms of "the composer holds
// operator text" and "it does not".
func draftPresent() bool { return true }
func draftAbsent() bool  { return false }

// countingDraft returns a lookup and a pointer to its resolution count. Each
// resolution is a real `tmux capture-pane` subprocess in production, so the count
// is the cost being measured.
func countingDraft(answer bool) (func() bool, *int) {
	n := 0
	return func() bool { n++; return answer }, &n
}

// The draft lookup must be resolved ONLY on the read that is deciding to act.
// Resolving it per read forks a capture-pane (3s timeout) for every wedged
// session on every 1-3s poll, inside the daemon's serial instance loop — and a
// transport outage wedges sessions in correlated batches. The anchor read and the
// confirm read can only return skip_dwell / skip_confirm, which never consult it.
func TestProcessRead_ComposerDraftLookup_ResolvedOnlyWhenDeciding(t *testing.T) {
	now := time.Unix(1780000000, 0).UTC()
	spy := &resumeSpy{outcome: "resumed:submitted"}
	e := resumeEngineFor(spy, &MemorySink{})
	lookup, calls := countingDraft(false)
	c := apiErrorCand(now)
	c.ComposerDraft = lookup

	if ev := e.ProcessRead(c, now.Add(apiReadAnchor)); ev.Decision != DecisionSkipDwell {
		t.Fatalf("premise broken: want %q, got %q", DecisionSkipDwell, ev.Decision)
	}
	if *calls != 0 {
		t.Fatalf("the anchor read cannot act and must capture nothing, got %d", *calls)
	}

	if ev := e.ProcessRead(c, now.Add(apiReadConfirm1)); ev.Decision != DecisionSkipConfirm {
		t.Fatalf("premise broken: want %q, got %q", DecisionSkipConfirm, ev.Decision)
	}
	if *calls != 0 {
		t.Fatalf("the confirm read cannot act and must capture nothing, got %d", *calls)
	}

	if ev := e.ProcessRead(c, now.Add(apiReadConfirm2)); ev.Decision != DecisionAct {
		t.Fatalf("premise broken: want %q, got %q", DecisionAct, ev.Decision)
	}
	if *calls != 1 {
		t.Fatalf("the deciding read resolves the lookup exactly once, got %d", *calls)
	}
}

// Observe mode is advertised as capture-free. Every read it takes over a wedged
// session that has not yet confirmed must resolve nothing: observe runs against
// the whole fleet by default, so a per-read capture there is the freeze.
func TestObserve_ComposerDraftLookup_NeverResolvedBeforeDeciding(t *testing.T) {
	now := time.Unix(1780000000, 0).UTC()
	e := NewObserveEngine(DefaultCaps(), &MemorySink{})
	lookup, calls := countingDraft(true)

	// Alternate the output signature so movement keeps every read short of the
	// confirm — the steady state of a fleet being polled every 1-3 seconds.
	for i := 0; i < 20; i++ {
		c := apiErrorCand(now)
		c.ComposerDraft = lookup
		c.OutputSig = []string{"sigA", "sigB"}[i%2]
		ev := e.ProcessRead(c, now.Add(apiReadAnchor+time.Duration(i)*2*time.Second))
		if ev.Decision == DecisionAct {
			t.Fatalf("read %d reached act; this test must stay on the non-deciding path", i)
		}
	}
	if *calls != 0 {
		t.Fatalf("observe must capture nothing on reads that cannot act, got %d", *calls)
	}
}

// The tail is what the two tests above cannot see: they stop at the first act,
// and the cost is AFTER it. The D6 branch sits above the safety machine, so a
// session that keeps clearing the two-read confirm reaches it forever — and that
// is exactly what a wedged session does once its recoveries are spent and its
// breaker is open. Measured against the unmemoised engine, a single session
// wedged for 30 minutes at a 2s poll cost 900 reads → 435 lookups, i.e. 435
// `tmux capture-pane` forks inside the daemon's serial instance loop, scaling
// with the poll rate and unbounded in time.
//
// The memo makes the rate a function of wall-clock instead: at most one lookup
// per composerDraftTTL.
func TestProcessRead_WedgedTail_BoundsTheDraftLookupRate(t *testing.T) {
	const (
		poll     = 2 * time.Second
		reads    = 900 // 30 minutes at a 2s poll
		unmemoed = 435 // the measured pre-fix lookup count for this exact drive
	)
	now := time.Unix(1780000000, 0).UTC()
	// A delivery that is NOT "resumed:submitted" counts as a failed recovery, so
	// the breaker opens after BreakerK=2 — the state the tail is about.
	spy := &resumeSpy{outcome: ResumeOutcome("typed_not_submitted")}
	e := resumeEngineFor(spy, &MemorySink{})
	lookup, calls := countingDraft(false)

	decisions := map[Decision]int{}
	for i := 0; i < reads; i++ {
		c := apiErrorCand(now)
		c.ComposerDraft = lookup
		ev := e.ProcessRead(c, now.Add(time.Duration(i)*poll))
		decisions[ev.Decision]++
	}

	if decisions[DecisionBreakerOpen] == 0 {
		t.Fatalf("premise broken: the tail must run with the breaker open, decisions=%v", decisions)
	}
	if len(spy.calls) == 0 {
		t.Fatalf("premise broken: the drive must poll PAST the first act, decisions=%v", decisions)
	}
	// Every read that clears the confirm still consults the branch — the fix is
	// not a peek at the gate, and this pins that it is not.
	if decisions[DecisionSkipConfirm] < unmemoed {
		t.Fatalf("premise broken: want >= %d confirm-clearing reads, got %d (decisions=%v)",
			unmemoed, decisions[DecisionSkipConfirm], decisions)
	}

	// One lookup per TTL over the drive's span, plus one for the first read.
	span := time.Duration(reads-1) * poll
	bound := int(span/composerDraftTTL) + 1
	if *calls > bound {
		t.Fatalf("lookups = %d, want <= %d (one per %s over %s)", *calls, bound, composerDraftTTL, span)
	}
	// And it must be a hard cut, not a rounding win.
	if *calls*2 > unmemoed {
		t.Fatalf("lookups = %d; the memo must cut the measured %d tail by more than half", *calls, unmemoed)
	}
	t.Logf("reads=%d draft_lookups=%d (was %d) decisions=%v", reads, *calls, unmemoed, decisions)
}

// The memo is bounded in the OTHER direction too: it may delay noticing that a
// draft appeared or cleared, but never by more than composerDraftTTL.
func TestProcessRead_DraftMemo_GoesStaleWithinTheTTL(t *testing.T) {
	now := time.Unix(1780000000, 0).UTC()
	spy := &resumeSpy{outcome: ResumeOutcome("submitted")}
	e := resumeEngineFor(spy, &MemorySink{})
	drafted := true
	c := apiErrorCand(now)
	c.ComposerDraft = func() bool { return drafted }

	e.ProcessRead(c, now.Add(apiReadAnchor))
	e.ProcessRead(c, now.Add(apiReadConfirm1))
	if ev := e.ProcessRead(c, now.Add(apiReadConfirm2)); ev.Outcome != "held_composer_draft" {
		t.Fatalf("premise broken: want held_composer_draft, got %q", ev.Outcome)
	}

	// The operator clears the draft. Drive confirm cycles from the moment the
	// memo was written until the engine acts; it must take no longer than the TTL.
	drafted = false
	memoAt := now.Add(apiReadConfirm2)
	for at := memoAt.Add(2 * time.Second); !at.After(memoAt.Add(composerDraftTTL + 4*time.Second)); at = at.Add(2 * time.Second) {
		if ev := e.ProcessRead(c, at); ev.Decision == DecisionAct && ev.Action == ActionResume {
			if stale := at.Sub(memoAt); stale > composerDraftTTL+2*time.Second {
				t.Fatalf("a cleared draft went unnoticed for %s, TTL is %s", stale, composerDraftTTL)
			}
			return
		}
	}
	t.Fatalf("a cleared draft was never noticed within %s of the memo", composerDraftTTL)
}

// The memo must be EVICTED when a session leaves the stuck class, not left to
// accumulate. The engine is long-lived per profile in a daemon that runs for
// weeks, so a memo that is only ever written leaves a permanent entry for every
// session the engine has ever evaluated.
func TestProcessRead_DraftMemo_EvictedWhenNoLongerACandidate(t *testing.T) {
	now := time.Unix(1780000000, 0).UTC()
	spy := &resumeSpy{outcome: ResumeOutcome("submitted")}
	e := resumeEngineFor(spy, &MemorySink{})
	c := apiErrorCand(now)
	c.ComposerDraft = func() bool { return true }

	e.ProcessRead(c, now.Add(apiReadAnchor))
	e.ProcessRead(c, now.Add(apiReadConfirm1))
	if ev := e.ProcessRead(c, now.Add(apiReadConfirm2)); ev.Outcome != "held_composer_draft" {
		t.Fatalf("premise broken: want held_composer_draft, got %q", ev.Outcome)
	}
	if _, ok := e.draftMemo[c.SessionID]; !ok {
		t.Fatal("premise broken: the deciding read must have written a memo")
	}

	// The session recovers: a healthy substate is not a candidate at all.
	healthy := apiErrorCand(now)
	healthy.Substate = tmux.SubstateNone
	healthy.ComposerDraft = func() bool { return true }
	e.ProcessRead(healthy, now.Add(apiReadConfirm2+2*time.Second))

	if _, ok := e.draftMemo[c.SessionID]; ok {
		t.Fatal("the memo must be evicted once the session leaves the stuck class")
	}
}

// A nil lookup — the caller had no way to look — reads as "no draft" and must not
// panic. Every non-resume substate carries one.
func TestProcessRead_NilComposerDraftLookup_ReadsAsNoDraft(t *testing.T) {
	now := time.Unix(1780000000, 0).UTC()
	spy := &resumeSpy{outcome: "resumed:submitted"}
	e := resumeEngineFor(spy, &MemorySink{})
	c := apiErrorCand(now)
	c.ComposerDraft = nil

	e.ProcessRead(c, now.Add(apiReadAnchor))
	e.ProcessRead(c, now.Add(apiReadConfirm1))
	ev := e.ProcessRead(c, now.Add(apiReadConfirm2))
	if ev.Outcome == "held_composer_draft" {
		t.Fatal("a missing lookup must not be read as a draft")
	}
	if len(spy.calls) != 1 {
		t.Fatalf("want exactly one resume, got %d", len(spy.calls))
	}
}

// §6 (policy bullet): a drafted composer yields ActionEscalate and never executes.
func TestProcessRead_DraftedComposer_EscalatesWithoutActing(t *testing.T) {
	now := time.Unix(1780000000, 0).UTC()
	spy := &resumeSpy{outcome: "resumed:submitted"}
	e := resumeEngineFor(spy, &MemorySink{})
	c := apiErrorCand(now)
	c.ComposerDraft = draftPresent

	e.ProcessRead(c, now.Add(apiReadAnchor))
	e.ProcessRead(c, now.Add(apiReadConfirm1))
	ev := e.ProcessRead(c, now.Add(apiReadConfirm2))
	if ev.WouldHave != ActionEscalate {
		t.Fatalf("a drafted composer must downgrade to %q, got %q", ActionEscalate, ev.WouldHave)
	}
	if ev.Action != ActionNone {
		t.Fatalf("a drafted composer must take no action, got %q", ev.Action)
	}
	if ev.Outcome != "held_composer_draft" {
		t.Fatalf("the audit must say a human's draft is what stopped it, got %q", ev.Outcome)
	}
	if len(spy.calls) != 0 {
		t.Fatal("autonomous code must never submit text a human typed")
	}
}

// The downgrade must not SPEND a recovery. Evaluated after RecordAttempt, a
// session parked behind an operator draft burns both of its 2-per-6h attempts
// doing nothing and is then cap-locked for six hours — so by the time the human
// clears the draft there is no budget left to resume it, which is the exact
// opposite of what D6 protects. This test fails if the downgrade is moved back
// below e.policy.Gate / e.policy.RecordAttempt.
func TestProcessRead_DraftedComposer_BurnsNoRecoveryBudget(t *testing.T) {
	now := time.Unix(1780000000, 0).UTC()
	spy := &resumeSpy{outcome: "resumed:submitted"}
	e := resumeEngineFor(spy, &MemorySink{})
	c := apiErrorCand(now)
	c.ComposerDraft = draftPresent

	// Anchor once; the engine's anchor persists while the substate holds and the
	// output signature does not move, so the dwell keeps growing from here.
	e.ProcessRead(c, now.Add(apiReadAnchor))

	// Six drafted confirm cycles — three times the per-session cap of 2.
	at := now.Add(apiReadConfirm1)
	for i := 0; i < 6; i++ {
		e.ProcessRead(c, at)
		ev := e.ProcessRead(c, at.Add(2*time.Second))
		if ev.Outcome != "held_composer_draft" {
			t.Fatalf("cycle %d: want held_composer_draft, got %q", i, ev.Outcome)
		}
		at = at.Add(4 * time.Second)
	}
	if len(spy.calls) != 0 {
		t.Fatalf("nothing may execute behind a draft, got %d calls", len(spy.calls))
	}

	// The operator clears their draft. The session must be resumable AT ONCE.
	c.ComposerDraft = draftAbsent
	e.ProcessRead(c, at)
	ev := e.ProcessRead(c, at.Add(2*time.Second))
	if ev.Decision != DecisionAct {
		t.Fatalf("a cleared draft must be immediately resumable, got %q (cap-locked?)", ev.Decision)
	}
	if len(spy.calls) != 1 {
		t.Fatalf("want exactly one resume once the draft cleared, got %d", len(spy.calls))
	}
}

// A future NotBefore keeps a usage-limit candidate out of the executor entirely.
func TestProcessRead_UsageLimit_NotBefore_DoesNotExecute(t *testing.T) {
	now := time.Unix(1780000000, 0).UTC()
	spy := &resumeSpy{outcome: "resumed:submitted"}
	e := resumeEngineFor(spy, &MemorySink{})
	c := Candidate{
		SessionID:       "s1",
		Substate:        tmux.SubstateUsageLimit,
		StatusChangedAt: now.Add(-time.Minute),
		NotBefore:       now.Add(2 * time.Hour),
		OutputSig:       "sig-stable",
	}
	for i := 0; i < 4; i++ {
		ev := e.ProcessRead(c, now.Add(time.Duration(i)*2*time.Second))
		if ev.Decision != DecisionSkipNotBefore {
			t.Fatalf("read %d: want %q, got %q", i, DecisionSkipNotBefore, ev.Decision)
		}
	}
	if len(spy.calls) != 0 {
		t.Fatalf("a scheduled candidate must not be executed early, got %d calls", len(spy.calls))
	}
}

// usage-limit carries the reason the executor needs to warn about a terminated
// subagent.
func TestProcessRead_UsageLimit_PastNotBefore_ReasonIsUsageLimit(t *testing.T) {
	now := time.Unix(1780000000, 0).UTC()
	spy := &resumeSpy{outcome: "resumed:submitted"}
	e := resumeEngineFor(spy, &MemorySink{})
	c := Candidate{
		SessionID:       "s1",
		Substate:        tmux.SubstateUsageLimit,
		StatusChangedAt: now.Add(-time.Minute),
		NotBefore:       now.Add(-time.Second),
		OutputSig:       "sig-stable",
	}
	e.ProcessRead(c, now)
	ev := e.ProcessRead(c, now.Add(2*time.Second))
	if ev.Action != ActionResume {
		t.Fatalf("want %q, got %q", ActionResume, ev.Action)
	}
	if got := ev.ActionParams["reason"]; got != "usage_limit" {
		t.Fatalf("want reason usage_limit, got %v", got)
	}
}

// A typed-but-unsubmitted delivery is recorded as a FAILURE, not a success, and
// feeds the circuit breaker.
func TestProcessRead_TypedNotSubmitted_CountsAsFailedRecovery(t *testing.T) {
	now := time.Unix(1780000000, 0).UTC()
	spy := &resumeSpy{outcome: "resumed:typed_not_submitted"}
	e := resumeEngineFor(spy, &MemorySink{})
	c := apiErrorCand(now)

	e.ProcessRead(c, now.Add(apiReadAnchor))
	e.ProcessRead(c, now.Add(apiReadConfirm1))
	ev := e.ProcessRead(c, now.Add(apiReadConfirm2))
	if ev.Outcome != "resumed:typed_not_submitted" {
		t.Fatalf("the audit must carry the real delivery, got %q", ev.Outcome)
	}
	// K for a non-auth class is 2: one more failed recovery opens the breaker.
	// The dwell is long satisfied by now, so this is confirm+act, not anchor+confirm.
	e.ProcessRead(c, now.Add(65*time.Second))
	e.ProcessRead(c, now.Add(67*time.Second))
	if !e.Policy().IsQuarantined("s1") {
		t.Fatal("two consecutive undelivered resumes must open the breaker")
	}
}

// An executor error is surfaced in the audit and takes no action.
func TestProcessRead_ExecutorError_RecordedNoAction(t *testing.T) {
	now := time.Unix(1780000000, 0).UTC()
	spy := &resumeSpy{err: errors.New("pane is gone")}
	e := resumeEngineFor(spy, &MemorySink{})
	c := apiErrorCand(now)

	e.ProcessRead(c, now.Add(apiReadAnchor))
	e.ProcessRead(c, now.Add(apiReadConfirm1))
	ev := e.ProcessRead(c, now.Add(apiReadConfirm2))
	if ev.Action != ActionNone {
		t.Fatalf("a failed execution takes no action, got %q", ev.Action)
	}
	if ev.Outcome != "error:pane is gone" {
		t.Fatalf("want the error surfaced in the outcome, got %q", ev.Outcome)
	}
}
