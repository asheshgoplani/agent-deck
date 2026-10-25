package session

import (
	"fmt"
	"strings"
	"testing"
	"time"
)

// Field bug STALEERR1019: a clean idle restart paged the parent with
// "Restarted, but the resumed session reports an error." One daemon pass
// judges the same flip twice: recordTerminalTurns (a new error|signal key)
// and then the snapshot loop (waiting -> error). The first judgement held the
// old process's exit; the second found the hold already spent ("only one exit
// is ours") and called the very same old SessionEnd an error of the new
// process. A later pass that re-reads the same light (the transcript signal
// moved while the old SessionEnd is still on file) did the same.
func TestStaleErr1019_SameOldExitSeenTwiceIsHeld(t *testing.T) {
	for _, base := range []time.Duration{900 * time.Millisecond, 100 * time.Millisecond} {
		t.Run(fmt.Sprintf("x.%03d", base.Milliseconds()), func(t *testing.T) {
			f := newTurnTestFixture(t)
			st := stubRestartPIDs(t)
			n := f.finishedTurn(t)
			sends := *f.sends
			// In the past: recordTerminalTurns stamps its flip with time.Now().
			respawn := time.Now().Add(-3 * time.Second).Truncate(time.Second).Add(base)
			st.realRestart(f, respawn, 20*time.Millisecond, 150*time.Millisecond)

			// One pass, as syncProfile runs it: recordTerminalTurns first,
			// then the snapshot loop's observed waiting -> error flip.
			statuses := map[string]string{f.child.ID: "error"} // the parent's own baseline is not under test
			f.d.recordTerminalTurns("default", f.byID, statuses, nil)
			f.d.emitTurn("default", f.child, f.byID, "waiting", "error", respawn.Add(time.Second), true)
			// A later pass: the resume appended a non-turn record (the signal
			// moved), the light still reads the same old SessionEnd.
			f.appendTurn(t, fxResumeMeta())
			f.d.recordTerminalTurns("default", f.byID, statuses, nil)

			for _, r := range f.inboxRecords(t)[n:] {
				if r.IsUrgent() || strings.Contains(r.Text, "reports an error") {
					t.Fatalf("false urgent on a clean idle restart: %q", r.Text)
				}
			}
			if *f.sends != sends {
				t.Fatalf("no wake, sends %d -> %d: %+v", sends, *f.sends, f.inboxRecords(t)[n:])
			}
			// The new process at its prompt is still the one info line.
			st.newHook("waiting", "SessionStart", respawn)
			f.bootSeen()
			f.d.emitTurn("default", f.child, f.byID, "running", "waiting", respawn.Add(3*time.Second), true)
			added := f.inboxRecords(t)[n:]
			if len(added) != 1 || added[0].IsUrgent() || !strings.Contains(added[0].Text, "conversation resumed") {
				t.Fatalf("want exactly the one info line, got %+v", added)
			}
		})
	}
}

// The hold is per exit, not per pass: an error with other evidence after the
// held one (the new process's own SessionEnd, or no old SessionEnd on file
// any more) still reaches the parent urgently.
func TestStaleErr1019_OtherErrorAfterTheHeldExitIsNews(t *testing.T) {
	cases := map[string]func(st *pidStub, at time.Time){
		"new SessionStart then an error": func(st *pidStub, at time.Time) { st.newHook("waiting", "SessionStart", at) },
		"hook file gone":                 func(st *pidStub, at time.Time) { st.hook = nil },
	}
	for name, change := range cases {
		t.Run(name, func(t *testing.T) {
			f := newTurnTestFixture(t)
			st := stubRestartPIDs(t)
			f.finishedTurn(t)
			sends := *f.sends
			at := time.Now()
			st.restart(f, at)
			f.d.emitTurn("default", f.child, f.byID, "waiting", "error", at, true) // the old exit, held
			if errs := f.recordsTo(t, "error"); len(errs) != 0 {
				t.Fatalf("precondition: the old exit is held: %+v", errs)
			}
			change(st, at)
			f.d.emitTurn("default", f.child, f.byID, "waiting", "error", at.Add(3*time.Second), true)
			errs := f.recordsTo(t, "error")
			if len(errs) != 1 || !errs[0].IsUrgent() || *f.sends == sends {
				t.Fatalf("a different error after the held exit is news: %+v", f.inboxRecords(t))
			}
		})
	}
}

// A different old-looking SessionEnd after the held one (still stamped within
// a second of the spawn record, the new process alive) is not the exit that
// was held: the hold covers one SessionEnd, so this one pages.
func TestStaleErr1019_DifferentSessionEndAfterTheHeldExitIsNews(t *testing.T) {
	f := newTurnTestFixture(t)
	st := stubRestartPIDs(t)
	f.finishedTurn(t)
	sends := *f.sends
	at := time.Now()
	st.restart(f, at)
	f.d.emitTurn("default", f.child, f.byID, "waiting", "error", at, true) // S1, held
	if errs := f.recordsTo(t, "error"); len(errs) != 0 {
		t.Fatalf("precondition: the old exit is held: %+v", errs)
	}
	s2 := at.Truncate(time.Second).Add(time.Second) // != S1, <= SpawnedAt+1s
	st.hook = &HookStatus{Status: "dead", Event: "SessionEnd", UpdatedAt: s2}
	f.d.emitTurn("default", f.child, f.byID, "waiting", "error", at.Add(2*time.Second), true)
	errs := f.recordsTo(t, "error")
	if len(errs) != 1 || !errs[0].IsUrgent() || *f.sends == sends {
		t.Fatalf("another SessionEnd after the held one is news: %+v", f.inboxRecords(t))
	}
}

// The hold lasts per token. A token can be removed by paths other than
// spendRestartToken (the notifier's boot record, the next restart, the
// sweep), so a hold from restart 1 must not judge restart 2's own old
// SessionEnd: restart 2 holds it, no urgent.
func TestStaleErr1019_HoldFromAnEarlierTokenDoesNotJudgeTheNext(t *testing.T) {
	cases := map[string]func(t *testing.T, f *turnTestFixture, at time.Time){
		"the notifier's boot record removes token 1": func(t *testing.T, f *turnTestFixture, at time.Time) {
			if !restartBootRecord(TransitionNotificationEvent{ChildSessionID: f.child.ID, ToStatus: "waiting", Timestamp: at.Add(time.Second)}) {
				t.Fatal("precondition: the boot record removes the token")
			}
		},
		"the next restart replaces token 1": func(*testing.T, *turnTestFixture, time.Time) {},
	}
	for name, drop := range cases {
		t.Run(name, func(t *testing.T) {
			f := newTurnTestFixture(t)
			st := stubRestartPIDs(t)
			n := f.finishedTurn(t)
			sends := *f.sends
			at1 := time.Now().Add(-10 * time.Second)
			st.restart(f, at1)
			f.d.emitTurn("default", f.child, f.byID, "waiting", "error", at1, true) // S1, held
			drop(t, f, at1)
			at2 := at1.Add(5 * time.Second)
			st.restart(f, at2) // a new token; S2 is its old process's SessionEnd
			f.d.emitTurn("default", f.child, f.byID, "waiting", "error", at2, true)
			for _, r := range f.inboxRecords(t)[n:] {
				if r.IsUrgent() || strings.Contains(r.Text, "reports an error") {
					t.Fatalf("false urgent on restart 2's own old exit: %q", r.Text)
				}
			}
			if *f.sends != sends {
				t.Fatalf("no wake, sends %d -> %d: %+v", sends, *f.sends, f.inboxRecords(t)[n:])
			}
		})
	}
}
