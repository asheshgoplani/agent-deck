package session

import (
	"fmt"
	"os"
	"strings"
	"testing"
	"time"
)

// Issue #2792: a restart of a child idle at its prompt reached the parent as
// news, through every path: the old process exiting (an urgent error) and the
// new one at its prompt (the old reply again). The restart leaves a token
// (old pid, new pid, transcript end); only the old process's exit and the
// new one's first arrival at its prompt with no new turn are held back.

// fxResumeMeta is the kind of non-turn record a resumed Claude appends to its
// transcript while it boots.
func fxResumeMeta() string { return `{"type":"permission-mode","permissionMode":"default"}` }

// pidStub stands in for the child's pane pids.
type pidStub struct {
	pane  int
	alive map[int]bool
}

func stubRestartPIDs(t *testing.T) *pidStub {
	t.Helper()
	st := &pidStub{pane: 100, alive: map[int]bool{100: true}}
	oldPane, oldAlive := instancePanePID, restartPIDAlive
	instancePanePID = func(*Instance) int { return st.pane }
	restartPIDAlive = func(pid int) bool { return st.alive[pid] }
	t.Cleanup(func() { instancePanePID, restartPIDAlive = oldPane, oldAlive })
	return st
}

// restart does what restartRecorded does around a respawn: token, the old
// process killed, the new one spawned and recorded.
func (st *pidStub) restart(f *turnTestFixture, at time.Time) {
	noteRestartToken(f.child, at)
	st.alive[st.pane] = false
	st.pane += 100
	st.alive[st.pane] = true
	markRestartSpawned(f.child, at)
}

// bootSeen is the daemon pass that sees the restarted child running while it
// boots, which starts a new run for it.
func (f *turnTestFixture) bootSeen() {
	f.d.forgetJournaledTurnsOfRunning("default", map[string]string{f.child.ID: "running", f.parent.ID: "waiting"})
}

// finishedTurn gives the child a real turn its parent has already received.
func (f *turnTestFixture) finishedTurn(t *testing.T) int {
	t.Helper()
	f.appendTurn(t, fxHuman("u1", "add the index"), fxAssistantText("a1", "Index added; tests pass."))
	f.d.emitTurn("default", f.child, f.byID, "running", "waiting", time.Now(), true)
	n := len(f.inboxRecords(t))
	if n != 1 {
		t.Fatalf("precondition: the real turn is one record, got %d", n)
	}
	return n
}

func (f *turnTestFixture) recordsTo(t *testing.T, status string) []TransitionNotificationEvent {
	var out []TransitionNotificationEvent
	for _, r := range f.inboxRecords(t) {
		if r.ToStatus == status {
			out = append(out, r)
		}
	}
	return out
}

// An idle restart, seen by either path: the old exit is held, the boot is one
// info line, nothing is urgent, nothing wakes, the old reply is not re-sent.
func TestIssue2792_IdleRestartIsOneInfoLine(t *testing.T) {
	for _, observed := range []bool{true, false} {
		t.Run(fmt.Sprintf("observed=%v", observed), func(t *testing.T) {
			f := newTurnTestFixture(t)
			st := stubRestartPIDs(t)
			n := f.finishedTurn(t)
			sends := *f.sends

			st.restart(f, time.Now())
			f.d.emitTurn("default", f.child, f.byID, "running", "error", time.Now(), observed) // the old process exits
			f.appendTurn(t, fxResumeMeta())
			f.bootSeen()
			f.d.emitTurn("default", f.child, f.byID, "running", "waiting", time.Now(), observed) // the new one at its prompt

			added := f.inboxRecords(t)[n:]
			if len(added) != 1 || added[0].IsUrgent() || added[0].Text != "Restarted while idle; conversation resumed, no new turn." {
				t.Fatalf("want exactly the one info line, got %+v", added)
			}
			if *f.sends != sends {
				t.Fatalf("no wake, sends %d -> %d", sends, *f.sends)
			}
			if _, left := readRestartToken(f.child.ID); left {
				t.Fatalf("the boot spends the token")
			}
		})
	}
}

// The idle build refresh names the build it moved the child onto.
func TestIssue2792_IdleRefreshNamesTheBuild(t *testing.T) {
	f := newTurnTestFixture(t)
	st := stubRestartPIDs(t)
	n := f.finishedTurn(t)
	f.child.SetRestartReason(RestartReasonIdleBuildRefresh)
	st.restart(f, time.Now())
	f.d.noteRestartBuild(f.child.ID, "/opt/claude/versions/2.1.303")
	f.appendTurn(t, fxResumeMeta())
	f.bootSeen()
	f.d.emitTurn("default", f.child, f.byID, "running", "waiting", time.Now(), true)
	added := f.inboxRecords(t)[n:]
	if len(added) != 1 || !strings.Contains(added[0].Text, "Refreshed onto claude 2.1.303") {
		t.Fatalf("want the refresh line naming the build, got %+v", added)
	}
}

// The live case: a transcript ending in a compaction summary with no reply
// has no turn identity and is keyed on its size, which the resume grows.
func TestIssue2792_CompactedTranscriptRestartIsQuiet(t *testing.T) {
	f := newTurnTestFixture(t)
	st := stubRestartPIDs(t)
	f.appendTurn(t, fxHuman("u1", "add the index"), fxAssistantText("a1", "Index added; tests pass."),
		`{"type":"system","subtype":"compact_boundary","isSidechain":false}`,
		fxUser("u2", "This session is being continued from a previous conversation.", map[string]any{"isCompactSummary": true}))
	settle := func() {
		past := time.Now().Add(-time.Hour)
		_ = os.Chtimes(f.transcript, past, past)
	}
	settle()
	sends := *f.sends
	st.restart(f, time.Now())
	f.d.emitTurn("default", f.child, f.byID, "running", "error", time.Now(), false)
	f.appendTurn(t, `{"type":"last-prompt"}`, `{"type":"custom-title","customTitle":"x"}`, fxResumeMeta(), `{"type":"cost-state"}`)
	settle()
	f.bootSeen()
	f.d.emitTurn("default", f.child, f.byID, "running", "waiting", time.Now(), true)
	recs := f.inboxRecords(t)
	if len(recs) != 1 || recs[0].IsUrgent() {
		t.Fatalf("want only the info line, got %+v", recs)
	}
	if *f.sends != sends {
		t.Fatalf("no wake, sends %d -> %d", sends, *f.sends)
	}
}

// A restart in the middle of a turn leaves no token: the parent hears that the
// child stopped, on either path (lander review of #2793).
func TestIssue2792_MidTurnRestartStillNotifies(t *testing.T) {
	cases := map[string]func(f *turnTestFixture, t *testing.T){
		"tool call after the text": func(f *turnTestFixture, t *testing.T) {
			f.appendTurn(t, fxHuman("u1", "run it"), fxAssistantText("a1", "Running the migration now."), fxAssistantToolUse("a2"))
		},
		"running": func(f *turnTestFixture, t *testing.T) {
			f.appendTurn(t, fxHuman("u1", "run it"), fxAssistantText("a1", "Running the migration now."))
			f.child.Status = StatusRunning
		},
	}
	for name, setup := range cases {
		for _, observed := range []bool{true, false} {
			t.Run(fmt.Sprintf("%s/observed=%v", name, observed), func(t *testing.T) {
				f := newTurnTestFixture(t)
				st := stubRestartPIDs(t)
				setup(f, t)
				st.restart(f, time.Now())
				if _, ok := readRestartToken(f.child.ID); ok {
					t.Fatalf("a child in the middle of a turn gets no token")
				}
				f.child.Status = StatusWaiting
				f.appendTurn(t, fxResumeMeta())
				f.bootSeen()
				f.d.emitTurn("default", f.child, f.byID, "running", "waiting", time.Now(), observed)
				if got := len(f.inboxRecords(t)); got != 1 {
					t.Fatalf("the parent must hear the cut-short child stopped: %d records", got)
				}
			})
		}
	}
}

// A real turn after the restart is delivered as before.
func TestIssue2792_NewTurnAfterRestartNotifies(t *testing.T) {
	f := newTurnTestFixture(t)
	st := stubRestartPIDs(t)
	f.finishedTurn(t)
	st.restart(f, time.Now())
	f.bootSeen()
	f.appendTurn(t, fxResumeMeta(), fxHuman("u2", "status?"), fxAssistantText("a2", "Blocked on the schema. Should I drop the old column?"))
	f.d.emitTurn("default", f.child, f.byID, "running", "waiting", time.Now(), true)
	var found bool
	for _, r := range f.inboxRecords(t) {
		found = found || (r.TurnUUID == "a2" && r.IsUrgent())
	}
	if !found {
		t.Fatalf("the question after the restart must reach the parent urgently: %+v", f.inboxRecords(t))
	}
}

// Any error of the new process is news, at any time, on either path.
func TestIssue2792_NewProcessErrorsAreNews(t *testing.T) {
	cases := map[string]func(t *testing.T, f *turnTestFixture, st *pidStub, observed bool){
		// the resume dies before it reaches its prompt (bad build, auth, no conversation)
		"crash after spawn": func(t *testing.T, f *turnTestFixture, st *pidStub, observed bool) {
			st.alive[st.pane] = false
			f.d.emitTurn("default", f.child, f.byID, "running", "error", time.Now(), observed)
		},
		// lander probe P1a: boot, then the resume dies a few seconds later
		"crash soon after boot": func(t *testing.T, f *turnTestFixture, st *pidStub, observed bool) {
			f.appendTurn(t, fxResumeMeta())
			f.bootSeen()
			f.d.emitTurn("default", f.child, f.byID, "running", "waiting", time.Now(), observed)
			st.alive[st.pane] = false
			f.d.emitTurn("default", f.child, f.byID, "running", "error", time.Now().Add(3*time.Second), observed)
		},
		// lander probe P1b: much later, long after the restart
		"error minutes later": func(t *testing.T, f *turnTestFixture, st *pidStub, observed bool) {
			f.appendTurn(t, fxResumeMeta())
			f.bootSeen()
			f.d.emitTurn("default", f.child, f.byID, "running", "waiting", time.Now(), observed)
			f.d.emitTurn("default", f.child, f.byID, "running", "error", time.Now().Add(5*time.Minute), observed)
		},
		// a second error while the new process lives: only one exit is ours
		"second error": func(t *testing.T, f *turnTestFixture, st *pidStub, observed bool) {
			f.d.emitTurn("default", f.child, f.byID, "running", "error", time.Now(), observed)
			f.d.emitTurn("default", f.child, f.byID, "running", "error", time.Now().Add(time.Second), observed)
		},
	}
	for name, run := range cases {
		for _, observed := range []bool{true, false} {
			t.Run(fmt.Sprintf("%s/observed=%v", name, observed), func(t *testing.T) {
				f := newTurnTestFixture(t)
				st := stubRestartPIDs(t)
				f.finishedTurn(t)
				sends := *f.sends
				st.restart(f, time.Now())
				run(t, f, st, observed)
				errs := f.recordsTo(t, "error")
				if len(errs) == 0 || !errs[0].IsUrgent() || *f.sends == sends {
					t.Fatalf("an error of the new process must reach the parent urgently: %+v", f.inboxRecords(t))
				}
			})
		}
	}
}

// An error before the restart recorded its new process waits for it.
func TestIssue2792_ErrorBeforeSpawnRecordedWaits(t *testing.T) {
	f := newTurnTestFixture(t)
	st := stubRestartPIDs(t)
	f.finishedTurn(t)
	noteRestartToken(f.child, time.Now())
	st.alive[st.pane] = false
	if _, ok := f.d.emitTurn("default", f.child, f.byID, "running", "error", time.Now(), false); ok {
		t.Fatalf("an error while the restart runs must be retried, not decided")
	}
	if errs := f.recordsTo(t, "error"); len(errs) != 0 {
		t.Fatalf("not delivered yet: %+v", errs)
	}
	st.pane += 100
	st.alive[st.pane] = true
	markRestartSpawned(f.child, time.Now())
	if _, ok := f.d.emitTurn("default", f.child, f.byID, "running", "error", time.Now(), false); !ok {
		t.Fatalf("once the new process is recorded the error is decided")
	}
	if errs := f.recordsTo(t, "error"); len(errs) != 0 {
		t.Fatalf("the old process's exit is held: %+v", errs)
	}
}

// A new process that does not reach its prompt within the boot timeout is
// reported once; one that came back without a seen flip is not.
func TestIssue2792_NeverBootsIsReported(t *testing.T) {
	t.Setenv("AGENTDECK_RESTART_BOOT_TIMEOUT", "1m")
	cases := []struct {
		name     string
		status   string
		newAlive bool
		want     bool
	}{
		{"new process died", "error", false, true},
		{"still booting", "running", true, true},
		{"stuck before its prompt", "starting", true, true},
		{"came back unseen", "waiting", true, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			f := newTurnTestFixture(t)
			st := stubRestartPIDs(t)
			f.finishedTurn(t)
			at := time.Now().Add(-2 * time.Minute)
			st.restart(f, at)
			st.alive[st.pane] = c.newAlive
			statuses := map[string]string{f.child.ID: c.status, f.parent.ID: "waiting"}
			f.d.sweepRestartTokens("default", f.byID, statuses, time.Now())
			f.d.sweepRestartTokens("default", f.byID, statuses, time.Now()) // once only
			errs := f.recordsTo(t, "error")
			if c.want && (len(errs) != 1 || !errs[0].IsUrgent() || !strings.Contains(errs[0].Text, "did not come back")) {
				t.Fatalf("want one urgent report, got %+v", f.inboxRecords(t))
			}
			if !c.want && len(errs) != 0 {
				t.Fatalf("a child that came back gets no report, got %+v", errs)
			}
			if _, left := readRestartToken(f.child.ID); left {
				t.Fatalf("the sweep spends the token")
			}
		})
	}
}

// Lander probe P3: the token is used once; a later flip with the same signal
// (a Codex-like child with no transcript) is delivered.
func TestIssue2792_TokenIsUsedOnce(t *testing.T) {
	f := newTurnTestFixture(t)
	stubRestartPIDs(t)
	writeRestartFile(f.child.ID, ".json", restartToken{At: time.Now(), Signal: "sig-1", OldPID: 100})
	writeRestartFile(f.child.ID, ".spawned.json", restartSpawned{NewPID: 100, At: time.Now()})
	if _, v := f.d.judgeRestart(f.child.ID, "", "sig-1", "waiting", time.Now().Add(time.Second)); v != restartBoot {
		t.Fatalf("the first arrival at the prompt is the boot, got %v", v)
	}
	if _, v := f.d.judgeRestart(f.child.ID, "", "sig-1", "waiting", time.Now().Add(4*time.Minute)); v != restartNews {
		t.Fatalf("a later flip with the same signal is news, got %v", v)
	}
}

// The notifier holds other producers to the boot rule; it never drops an
// error or a finished record (lander probe P2).
func TestIssue2792_NotifierBackstop(t *testing.T) {
	f := newTurnTestFixture(t)
	st := stubRestartPIDs(t)
	f.appendTurn(t, fxHuman("u1", "add the index"), fxAssistantText("a1", "Index added; tests pass."))
	st.restart(f, time.Now())
	f.appendTurn(t, fxResumeMeta())
	ev := TransitionNotificationEvent{ChildSessionID: f.child.ID, ChildTitle: f.child.Title, Profile: "default",
		FromStatus: "running", ToStatus: "waiting", Timestamp: time.Now().Add(time.Second), Text: "Index added; tests pass."}
	if r := f.d.notifier.NotifyTransition(ev); r.DeliveryResult == transitionDeliveryCommitted {
		t.Fatalf("the notifier delivered a restart boot: %+v", r)
	}
	st.restart(f, time.Now())
	done := TransitionNotificationEvent{ChildSessionID: f.child.ID, ChildTitle: f.child.Title, Profile: "default",
		Timestamp: time.Now().Add(time.Second), DoneStatus: "ok", DoneSummary: "external done"}
	if r := f.d.notifier.NotifyFinished(done); r.DeliveryResult != transitionDeliveryCommitted {
		t.Fatalf("a finished record is always delivered: %v", r.DeliveryResult)
	}
	errEv := ev
	errEv.ToStatus, errEv.Timestamp = "error", time.Now().Add(2*time.Second)
	if r := f.d.notifier.NotifyTransition(errEv); r.DeliveryResult != transitionDeliveryCommitted {
		t.Fatalf("the notifier never drops an error: %v", r.DeliveryResult)
	}
}

// Lander probe P4: the new pid is the restart's own file, so nothing the
// daemon writes can erase it.
func TestIssue2792_SpawnedPIDSurvivesDaemonWork(t *testing.T) {
	f := newTurnTestFixture(t)
	st := stubRestartPIDs(t)
	f.finishedTurn(t)
	st.restart(f, time.Now())
	f.d.emitTurn("default", f.child, f.byID, "running", "error", time.Now(), false)
	tok, ok := readRestartToken(f.child.ID)
	if !ok || tok.NewPID != st.pane {
		t.Fatalf("the recorded new pid must survive the daemon's judging: %+v ok=%v", tok, ok)
	}
}
