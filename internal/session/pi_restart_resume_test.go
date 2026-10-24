package session

import (
	"strings"
	"testing"
	"time"
)

// A settles-capable pi child idle at its prompt has a turn signal (its last run's
// pi-run: key), so a restart now leaves it a token (#2792), as it does a Claude or
// Codex child. The kill writes nothing (real pi 1.4.0 writes no session_shutdown
// under respawn-pane -k); the resumed pi's session_start (waiting) is the only
// lifecycle write. It rebuilds the status file with no run, carrying the last run
// forward in pi_orphan_runs, so the boot flip and the sweep must find the token's
// run there.

// usePiHookFile makes the restart judge read the hook file the piWriter writes,
// in place of pidStub's hand-made one.
func usePiHookFile(t *testing.T) {
	t.Helper()
	old := restartHookStatus
	restartHookStatus = readHookStatusFile
	t.Cleanup(func() { restartHookStatus = old })
}

// piRestart is pidStub.restart for a pi child: the token, the old pi killed (it
// writes nothing), the new one spawned and recorded. The writer's next event (the
// resumed pi's) comes after the spawn record.
func (st *pidStub) piRestart(f *turnTestFixture, w *piWriter, at time.Time) {
	noteRestartToken(f.child, at)
	st.alive[st.pane] = false
	st.pane += 100
	st.alive[st.pane] = true
	markRestartSpawned(f.child, at.Add(10*time.Millisecond))
	w.at = at.Add(20 * time.Millisecond)
}

// An idle restart of a settles-capable pi child, through the daemon's poll: the
// resumed pi's session_start changes the poll's key (waiting|pi-run:<A> to
// waiting|), and that flip is the one info line; nothing wakes. A second idle
// restart with no run between writes only another session_start, which is no
// status edge (the key stays waiting|): no line, no wake, and the sweep retires
// the token at the boot timeout, as an unseen boot is on main. The next run rings
// the parent as before.
func TestPiRestart_IdleRestartIsOneInfoLine(t *testing.T) {
	t.Setenv("AGENTDECK_RESTART_BOOT_TIMEOUT", "1m")
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
		t.Fatalf("restart 1: a settles-capable pi child idle at its prompt gets a restart token")
	}
	w.SessionStart() // the resumed pi at its prompt
	f.piPoll()
	added := f.inboxRecords(t)[1:]
	if len(added) != 1 || added[0].IsUrgent() || added[0].Text != "Restarted while idle; conversation resumed, no new turn." {
		t.Fatalf("restart 1: want exactly the one info line, got %+v", added)
	}
	if *f.sends != 1 {
		t.Fatalf("restart 1: the restart wakes nobody, sends %d", *f.sends)
	}
	if _, left := readRestartToken(f.child.ID); left {
		t.Fatalf("restart 1: the boot spends the token")
	}

	st.piRestart(f, w, time.Now())
	if _, ok := readRestartToken(f.child.ID); !ok {
		t.Fatalf("restart 2: a pi child idle at a session_start gets a token from the carried run")
	}
	w.SessionStart()
	f.piPoll()
	f.wantRecords(t, "restart 2: a second session_start is no status edge", 2, 1)
	if _, ok := readRestartToken(f.child.ID); !ok {
		t.Fatalf("restart 2: no flip, so the token waits for the sweep")
	}
	statuses := map[string]string{f.child.ID: "waiting", f.parent.ID: "waiting"}
	f.d.sweepRestartTokens("default", f.byID, statuses, time.Now().Add(2*time.Minute))
	if errs := f.recordsTo(t, "error"); len(errs) != 0 {
		t.Fatalf("restart 2: a pi child back at its prompt gets no report, got %+v", errs)
	}
	if _, left := readRestartToken(f.child.ID); left {
		t.Fatalf("restart 2: the sweep retires the token past the boot timeout")
	}

	w.TurnStart("run-B")
	f.piPoll()
	w.TurnEnd("run-B").Settle("run-B", "entry-2")
	f.piPoll()
	f.wantRecords(t, "the first run after the restarts", 3, 2)
}

// The sweep past the boot timeout: a resumed pi back at its prompt (its
// session_start unseen) or already on a new run gets nothing, even while its
// status reads running (past the 2-minute hook window the status comes from the
// pane); one still booting (no session_start yet) or dead is reported once.
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
		{"back at its prompt, status running", func(w *piWriter) { w.SessionStart() }, "running", true, false},
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

// A daemon restarted along with the child replays the runs the file carries
// before it judges the boot (recordTerminalTurns runs recordOrphanedPiRuns first).
// A run aborted before its turn_end has no turn_end time, so its replay is
// stamped now, after the token. A replayed run is not the restart's boot and
// must not spend the token: the resumed pi at its prompt is still the info line.
func TestPiRestart_AnOrphanReplayDoesNotSpendTheToken(t *testing.T) {
	f := newTurnTestFixture(t)
	piChild(f)
	st := stubRestartPIDs(t)
	usePiHookFile(t)
	w := newPiWriter(t, f.child.ID, true)
	w.TurnStart("run-0").TurnStartAfterAbort("run-A") // run-0 aborted before its turn_end
	f.piPoll()
	w.TurnEnd("run-A").Settle("run-A", "entry-1")
	f.piPoll()
	n, sends := len(f.inboxRecords(t)), *f.sends

	f.restartDaemon() // no memory of the replays it already made
	st.piRestart(f, w, time.Now())
	if _, ok := readRestartToken(f.child.ID); !ok {
		t.Fatalf("a settles-capable pi child idle at its prompt gets a restart token")
	}
	w.SessionStart()
	f.piPoll() // the orphans [run-0, run-A] replay first, then the session_start waiting

	added := f.inboxRecords(t)[n:]
	if len(added) != 1 || added[0].IsUrgent() || added[0].Text != "Restarted while idle; conversation resumed, no new turn." {
		t.Fatalf("want exactly the one info line, got %+v", added)
	}
	if *f.sends != sends {
		t.Fatalf("the restart wakes nobody, sends %d -> %d", sends, *f.sends)
	}
	if _, left := readRestartToken(f.child.ID); left {
		t.Fatalf("the boot spends the token")
	}
}

// The resumed pi can write its session_start before the restart records the new
// process (the restart's post-launch tmux work and DB write come after the
// launch). The kill writes nothing, so a session_start after the token is the
// resumed pi's: back at its prompt, even while the status reads running.
func TestPiRestart_ASessionStartBeforeTheSpawnRecordIsBack(t *testing.T) {
	t.Setenv("AGENTDECK_RESTART_BOOT_TIMEOUT", "1m")
	f := newTurnTestFixture(t)
	piChild(f)
	st := stubRestartPIDs(t)
	usePiHookFile(t)
	w := newPiWriter(t, f.child.ID, true).Age(3 * time.Minute)
	w.Run("run-A", "entry-1")
	at := time.Now().Add(-2 * time.Minute)
	noteRestartToken(f.child, at)
	st.alive[st.pane] = false // the kill writes nothing
	w.at = at
	st.pane += 100
	st.alive[st.pane] = true
	w.SessionStart() // the resumed pi, before the spawn record
	markRestartSpawned(f.child, at.Add(time.Second))
	statuses := map[string]string{f.child.ID: "running", f.parent.ID: "waiting"}
	f.d.sweepRestartTokens("default", f.byID, statuses, time.Now())
	if errs := f.recordsTo(t, "error"); len(errs) != 0 {
		t.Fatalf("a pi child back at its prompt gets no report, got %+v", errs)
	}
	if _, left := readRestartToken(f.child.ID); left {
		t.Fatalf("the sweep spends the token")
	}
}

// Only a lifecycle write (session_start, session_shutdown) names no run because it
// rebuilt the file. A turn_end with no run but a carried list (an extension
// downgraded to v3 under this binary: every write copies the list) is a real
// completion, not the boot.
func TestPiRestart_ADowngradedWritersTurnEndIsNotTheBoot(t *testing.T) {
	f := newTurnTestFixture(t)
	piChild(f)
	st := stubRestartPIDs(t)
	usePiHookFile(t)
	w := newPiWriter(t, f.child.ID, true)
	w.Run("run-A", "entry-1")
	st.piRestart(f, w, time.Now())
	if _, ok := readRestartToken(f.child.ID); !ok {
		t.Fatalf("a settles-capable pi child idle at its prompt gets a restart token")
	}
	w.settles = false // the resumed pi runs a v3 extension
	w.SessionStart().TurnStart("").TurnEnd("")
	if _, v, _ := f.d.judgeRestart(f.child.ID, "", "", func() string { return "" }, "waiting", time.Now()); v == restartBoot {
		t.Fatalf("a v3 turn_end with a carried list was judged the boot")
	}
}

// A run whose replay is stamped after the token: finished between the token and
// the kill, or cut short by it. The restarting process read a cached waiting
// status while pi was mid-run, so the token holds that run's key, and the daemon
// is down across the run's end. Once the resumed pi's session_start rebuilds the
// file, the run is carried and replayed: stamped at its turn_end when it had one,
// at the replay when the kill cut it short, after the token either way. The
// parent never received it: it is a real turn (one record, one wake), and the
// boot is still the info line. When the daemon comes up before the resumed pi's
// session_start, run A is still the file's current run, not a carried one: its
// flip matches the token and is judged the boot, so the parent gets a misleading
// info line first, then run A itself once the session_start carries it. No turn
// is lost; telling that turn_end apart (a turn_end of the token's run stamped
// after the token is news) is left out to keep the restart change small.
func TestPiRestart_ARunTheDaemonNeverSawIsDeliveredAcrossARestart(t *testing.T) {
	for _, c := range []struct {
		name                   string
		finished, resumedFirst bool
	}{
		{"session_start before the daemon", true, true},
		{"daemon before the session_start", true, false},
		{"cut short by the kill", false, true},
	} {
		t.Run(c.name, func(t *testing.T) {
			f := newTurnTestFixture(t)
			piChild(f)
			st := stubRestartPIDs(t)
			usePiHookFile(t)
			w := newPiWriter(t, f.child.ID, true).Age(time.Second)
			w.TurnStart("run-A")
			f.piPoll()
			f.wantRecords(t, "run A running", 0, 0)
			at := time.Now()
			noteRestartToken(f.child, at) // the cached status reads waiting
			if tok, ok := readRestartToken(f.child.ID); !ok || tok.Signal != piRunSignal("run-A") {
				t.Fatalf("want a token on run A, got %+v (ok=%v)", tok, ok)
			}
			w.at = at
			if c.finished {
				w.TurnEnd("run-A").Settle("run-A", "entry-1") // before the kill; the daemon is down
			}
			st.alive[st.pane] = false // the kill writes nothing
			st.pane += 100
			st.alive[st.pane] = true
			markRestartSpawned(f.child, at.Add(10*time.Millisecond))
			w.at = at.Add(20 * time.Millisecond)
			if c.resumedFirst {
				w.SessionStart()
			}
			f.restartDaemon()
			f.piPoll()
			if !c.resumedFirst {
				w.SessionStart()
				f.piPoll()
			}
			var runA, info int
			for _, r := range f.inboxRecords(t) {
				switch {
				case r.LastOutputHash == piRunSignal("run-A"):
					runA++
				case r.Text == "Restarted while idle; conversation resumed, no new turn.":
					info++
				}
			}
			if runA != 1 || *f.sends != 1 {
				t.Fatalf("run A delivered %d times (want 1), sends %d (want 1): %+v", runA, *f.sends, f.inboxRecords(t))
			}
			if info != 1 {
				t.Fatalf("one info line (misleading when the daemon came up first), got %d: %+v", info, f.inboxRecords(t))
			}
		})
	}
}
