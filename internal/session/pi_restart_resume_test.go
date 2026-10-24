package session

import (
	"fmt"
	"strings"
	"testing"
	"time"
)

// A settles-capable pi child idle at its prompt has a turn signal (its last run's
// pi-run: key), so a restart now leaves it a token (#2792), as it does a Claude or
// Codex child. pi spells the lifecycle its own way: the old process writes
// session_shutdown (status dead), the resumed one session_start (waiting). Both
// rebuild the status file with no run, carrying the last run forward in
// pi_orphan_runs, so the boot flip and the sweep must find the token's run there.

// usePiHookFile makes the restart judge read the hook file the piWriter writes,
// in place of pidStub's hand-made one.
func usePiHookFile(t *testing.T) {
	t.Helper()
	old := restartHookStatus
	restartHookStatus = readHookStatusFile
	t.Cleanup(func() { restartHookStatus = old })
}

// piRestart is pidStub.restart for a pi child: the token, the old pi killed (its
// session_shutdown), the new one spawned and recorded.
func (st *pidStub) piRestart(f *turnTestFixture, w *piWriter, at time.Time) {
	noteRestartToken(f.child, at)
	st.alive[st.pane] = false
	w.SessionShutdown()
	st.pane += 100
	st.alive[st.pane] = true
	markRestartSpawned(f.child, at)
}

// An idle restart of a settles-capable pi child: the old exit is held, the resumed
// pi's session_start at its prompt is the one info line, nothing wakes, and the
// next run rings the parent as before.
func TestPiRestart_IdleRestartIsOneInfoLine(t *testing.T) {
	for _, observed := range []bool{true, false} {
		t.Run(fmt.Sprintf("observed=%v", observed), func(t *testing.T) {
			f := newTurnTestFixture(t)
			piChild(f)
			st := stubRestartPIDs(t)
			usePiHookFile(t)
			w := newPiWriter(t, f.child.ID, true)
			w.TurnStart("run-A")
			f.piPoll()
			w.TurnEnd("run-A").Settle("run-A", "entry-1")
			f.piPoll()
			f.wantRecords(t, "the run before the restart", 1, 1)

			st.piRestart(f, w, time.Now())
			if _, ok := readRestartToken(f.child.ID); !ok {
				t.Fatalf("a settles-capable pi child idle at its prompt gets a restart token")
			}
			f.d.emitTurn("default", f.child, f.byID, "running", "error", time.Now(), observed) // the old pi exits
			w.SessionStart()                                                                   // the resumed pi at its prompt
			f.bootSeen()
			f.d.emitTurn("default", f.child, f.byID, "running", "waiting", time.Now(), observed)

			added := f.inboxRecords(t)[1:]
			if len(added) != 1 || added[0].IsUrgent() || added[0].Text != "Restarted while idle; conversation resumed, no new turn." {
				t.Fatalf("want exactly the one info line, got %+v", added)
			}
			if *f.sends != 1 {
				t.Fatalf("the restart wakes nobody, sends %d", *f.sends)
			}
			if _, left := readRestartToken(f.child.ID); left {
				t.Fatalf("the boot spends the token")
			}

			w.TurnStart("run-B")
			f.piPoll()
			w.TurnEnd("run-B").Settle("run-B", "entry-2")
			f.piPoll()
			f.wantRecords(t, "the first run after the restart", 3, 2)
		})
	}
}

// The sweep past the boot timeout: a resumed pi back at its prompt (its
// session_start unseen) or already on a new run gets nothing; one still booting
// (no session_start yet) or dead is reported once.
func TestPiRestart_SweepWaitsForTheSessionStart(t *testing.T) {
	t.Setenv("AGENTDECK_RESTART_BOOT_TIMEOUT", "1m")
	cases := []struct {
		name     string
		after    func(w *piWriter)
		status   string
		newAlive bool
		want     bool
	}{
		{"came back unseen", func(w *piWriter) { w.SessionStart() }, "waiting", true, false},
		{"on a new run", func(w *piWriter) { w.SessionStart().TurnStart("run-B") }, "running", true, false},
		{"still booting", func(*piWriter) {}, "running", true, true},
		{"new process died", func(*piWriter) {}, "error", false, true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			f := newTurnTestFixture(t)
			piChild(f)
			st := stubRestartPIDs(t)
			usePiHookFile(t)
			w := newPiWriter(t, f.child.ID, true).Age(3 * time.Minute)
			w.Run("run-A", "entry-1")
			st.piRestart(f, w, time.Now().Add(-2*time.Minute))
			if _, ok := readRestartToken(f.child.ID); !ok {
				t.Fatalf("a settles-capable pi child idle at its prompt gets a restart token")
			}
			c.after(w)
			st.alive[st.pane] = c.newAlive
			statuses := map[string]string{f.child.ID: c.status, f.parent.ID: "waiting"}
			f.d.sweepRestartTokens("default", f.byID, statuses, time.Now())
			f.d.sweepRestartTokens("default", f.byID, statuses, time.Now()) // once only
			errs := f.recordsTo(t, "error")
			if c.want && (len(errs) != 1 || !errs[0].IsUrgent() || !strings.Contains(errs[0].Text, "did not come back")) {
				t.Fatalf("want one urgent report, got %+v", f.inboxRecords(t))
			}
			if !c.want && len(errs) != 0 {
				t.Fatalf("a pi child that came back gets no report, got %+v", errs)
			}
			if _, left := readRestartToken(f.child.ID); left {
				t.Fatalf("the sweep spends the token")
			}
		})
	}
}
