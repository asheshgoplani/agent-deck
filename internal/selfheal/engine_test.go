package selfheal

import (
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
		e := &Engine{mode: m, caps: DefaultCaps(), policy: NewPolicyMachine(DefaultCaps()), sink: &MemorySink{}, exec: spy, prevSig: map[string]string{}, confirmed: map[string]confirmState{}, substateSeen: map[string]substateEntry{}}
		outcome, action := e.executeIfAuthorized(c, ActionRestartModelSwitch)
		if action != ActionNone {
			t.Fatalf("mode %q: HELD modes must take no action, got %q", m, action)
		}
		if outcome != "held_stage_2_3" {
			t.Fatalf("mode %q: want held_stage_2_3 outcome, got %q", m, outcome)
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
