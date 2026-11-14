package session

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
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

// pidStub stands in for the child's pane pids and hook file.
type pidStub struct {
	pane     int
	alive    map[int]bool
	hook     *HookStatus
	substate string
}

func stubRestartPIDs(t *testing.T) *pidStub {
	t.Helper()
	st := &pidStub{pane: 100, alive: map[int]bool{100: true}}
	oldPane, oldAlive, oldHook, oldDelay := instancePanePID, restartPIDAlive, restartHookStatus, restartPIDRetryDelay
	instancePanePID = func(*Instance) int { return st.pane }
	restartPIDAlive = func(pid int) bool { return st.alive[pid] }
	restartHookStatus = func(string) *HookStatus { return st.hook }
	oldSub := restartPaneSubstate
	restartPaneSubstate = func(*Instance) string { return st.substate }
	t.Cleanup(func() { restartPaneSubstate = oldSub })
	restartPIDRetryDelay = time.Millisecond
	t.Cleanup(func() {
		instancePanePID, restartPIDAlive, restartHookStatus, restartPIDRetryDelay = oldPane, oldAlive, oldHook, oldDelay
	})
	return st
}

// restart does what restartRecorded does around a respawn: token, the old
// process killed (its SessionEnd hook, whole seconds like the real file), the
// new one spawned and recorded.
func (st *pidStub) restart(f *turnTestFixture, at time.Time) {
	noteRestartToken(f.child, at)
	st.alive[st.pane] = false
	st.hook = &HookStatus{Status: "dead", Event: "SessionEnd", UpdatedAt: at.Truncate(time.Second)}
	st.pane += 100
	st.alive[st.pane] = true
	markRestartSpawned(f.child, at)
}

// newHook is a hook the resumed process writes after the restart.
func (st *pidStub) newHook(status, event string, at time.Time) {
	st.hook = &HookStatus{Status: status, Event: event, UpdatedAt: at.Add(2 * time.Second).Truncate(time.Second)}
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
		// lander probe Q1: respawn is atomic, no old exit seen; the live resumed
		// Claude shows an expired-login banner (pane status error)
		"auth banner, no old exit seen": func(t *testing.T, f *turnTestFixture, st *pidStub, observed bool) {
			st.hook = nil
			st.substate = "auth-401"
			f.appendTurn(t, fxResumeMeta())
			f.d.emitTurn("default", f.child, f.byID, "running", "error", time.Now(), observed)
		},
		// the same while the hook file still holds the old process's SessionEnd
		// (conductor ruling: an error state on a live pane is never the old exit)
		"auth banner with the old SessionEnd on file": func(t *testing.T, f *turnTestFixture, st *pidStub, observed bool) {
			st.substate = "auth-401"
			f.appendTurn(t, fxResumeMeta())
			f.d.emitTurn("default", f.child, f.byID, "running", "error", time.Now(), observed)
		},
		// model unavailable after the resumed Claude's SessionStart
		"model unavailable after its SessionStart": func(t *testing.T, f *turnTestFixture, st *pidStub, observed bool) {
			st.newHook("waiting", "SessionStart", time.Now())
			st.substate = "model-unavailable"
			f.appendTurn(t, fxResumeMeta())
			f.d.emitTurn("default", f.child, f.byID, "running", "error", time.Now().Add(3*time.Second), observed)
		},
		// the old pid is still alive: its SessionEnd is not an exit yet, and the
		// error cannot be told apart, so deliver it (conductor ruling)
		"old process still alive": func(t *testing.T, f *turnTestFixture, st *pidStub, observed bool) {
			st.alive[st.pane-100] = true
			f.d.emitTurn("default", f.child, f.byID, "running", "error", time.Now(), observed)
		},
		// an error that brings no exit evidence cannot be attributed: deliver it
		"error with no exit evidence": func(t *testing.T, f *turnTestFixture, st *pidStub, observed bool) {
			st.hook = nil
			f.d.emitTurn("default", f.child, f.byID, "running", "error", time.Now(), observed)
		},
		// a second error while the new process lives: only one exit is ours.
		// The same old SessionEnd seen again is that exit (STALEERR1019); an
		// error after the new process wrote its own hook is not.
		"second error": func(t *testing.T, f *turnTestFixture, st *pidStub, observed bool) {
			f.d.emitTurn("default", f.child, f.byID, "running", "error", time.Now(), observed)
			st.newHook("waiting", "SessionStart", time.Now())
			f.d.emitTurn("default", f.child, f.byID, "running", "error", time.Now().Add(3*time.Second), observed)
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
				if !strings.Contains(name, "after boot") && !strings.Contains(name, "minutes later") &&
					!strings.HasPrefix(errs[0].Text, "Restarted, but the resumed session") {
					t.Fatalf("the error says what is wrong, not the old reply: %q", errs[0].Text)
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
	st.hook = &HookStatus{Status: "dead", Event: "SessionEnd", UpdatedAt: time.Now().Truncate(time.Second)}
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
	if _, v, _ := f.d.judgeRestart(f.child.ID, "", "sig-1", func() string { return "" }, "waiting", time.Now().Add(time.Second)); v != restartBoot {
		t.Fatalf("the first arrival at the prompt is the boot, got %v", v)
	}
	if _, v, _ := f.d.judgeRestart(f.child.ID, "", "sig-1", func() string { return "" }, "waiting", time.Now().Add(4*time.Minute)); v != restartNews {
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

// Lander probe Q1b: an error the parent was not told about must not be
// papered over by a later "conversation resumed" line.
func TestIssue2792_AuthBannerThenPromptIsReported(t *testing.T) {
	f := newTurnTestFixture(t)
	st := stubRestartPIDs(t)
	f.finishedTurn(t)
	st.restart(f, time.Now())
	st.hook = nil
	st.substate = "auth-401"
	f.appendTurn(t, fxResumeMeta())
	f.d.emitTurn("default", f.child, f.byID, "running", "error", time.Now(), true)
	st.substate = ""
	f.bootSeen()
	f.d.emitTurn("default", f.child, f.byID, "running", "waiting", time.Now(), true)
	f.d.sweepRestartTokens("default", f.byID, map[string]string{f.child.ID: "waiting"}, time.Now().Add(10*time.Minute))
	urgent := 0
	for _, r := range f.inboxRecords(t) { // a later flip of the old turn reorders the inbox
		if r.IsUrgent() && r.ToStatus == "error" {
			urgent++
		}
		if strings.Contains(r.Text, "conversation resumed") {
			t.Fatalf("a restart that showed an error must not be reported as a clean resume: %+v", r)
		}
	}
	if urgent == 0 {
		t.Fatalf("the error must reach the parent: %+v", f.inboxRecords(t))
	}
}

// Lander probe Q2: a new pid that cannot be read holds nothing back and
// raises no false alarm for a child at its prompt; a child that is down is
// still reported.
func TestIssue2792_UnreadableNewPID(t *testing.T) {
	for _, c := range []struct {
		status string
		want   bool
	}{{"waiting", false}, {"error", true}} {
		t.Run(c.status, func(t *testing.T) {
			t.Setenv("AGENTDECK_RESTART_BOOT_TIMEOUT", "1m")
			f := newTurnTestFixture(t)
			st := stubRestartPIDs(t)
			f.finishedTurn(t)
			at := time.Now().Add(-3 * time.Minute)
			noteRestartToken(f.child, at)
			st.alive[st.pane] = false
			st.pane = 0 // list-panes failed, or the pane is gone
			markRestartSpawned(f.child, at)
			if tok, ok := readRestartToken(f.child.ID); !ok || tok.NewPID != restartPIDUnknown {
				t.Fatalf("an unreadable new pid is recorded as unknown: %+v ok=%v", tok, ok)
			}
			if _, ok := f.d.emitTurn("default", f.child, f.byID, "running", "error", time.Now(), true); !ok {
				t.Fatalf("flips are decided, not left unsettled")
			}
			if errs := f.recordsTo(t, "error"); len(errs) != 1 {
				t.Fatalf("an error with an unknown new pid is delivered: %+v", f.inboxRecords(t))
			}
			g := newTurnTestFixture(t)
			st2 := stubRestartPIDs(t)
			g.finishedTurn(t)
			noteRestartToken(g.child, at)
			st2.alive[st2.pane] = false
			st2.pane = 0
			markRestartSpawned(g.child, at)
			g.d.sweepRestartTokens("default", g.byID, map[string]string{g.child.ID: c.status}, time.Now())
			if got := len(g.recordsTo(t, "error")); (got == 1) != c.want {
				t.Fatalf("sweep with status %s: %d reports, want report=%v", c.status, got, c.want)
			}
		})
	}
}

// Lander probe Q3: the resumed Claude exits (its own SessionEnd) and the pane
// falls back to a shell prompt: not a clean resume.
func TestIssue2792_ResumedClaudeExitIsNotABoot(t *testing.T) {
	f := newTurnTestFixture(t)
	st := stubRestartPIDs(t)
	n := f.finishedTurn(t)
	st.restart(f, time.Now())
	st.newHook("dead", "SessionEnd", time.Now())
	f.bootSeen()
	f.d.emitTurn("default", f.child, f.byID, "running", "waiting", time.Now().Add(3*time.Second), true)
	for _, r := range f.inboxRecords(t)[n:] {
		if strings.Contains(r.Text, "conversation resumed") {
			t.Fatalf("an exited resume must not read as a clean one: %+v", r)
		}
	}
}

// Lander probe Q4: a daemon restart forgets what it held; the next real
// error still reaches the parent, because it brings its own evidence.
func TestIssue2792_DaemonRestartDoesNotRearmTheHold(t *testing.T) {
	f := newTurnTestFixture(t)
	st := stubRestartPIDs(t)
	f.finishedTurn(t)
	st.restart(f, time.Now())
	f.d.emitTurn("default", f.child, f.byID, "running", "error", time.Now(), true) // the old exit, held
	f.d.restart = nil
	f.d.restartOnce = sync.Once{}
	st.newHook("waiting", "SessionStart", time.Now()) // the resumed Claude starts
	st.substate = "auth-401"
	f.d.emitTurn("default", f.child, f.byID, "running", "error", time.Now().Add(3*time.Second), true) // then shows an error
	if len(f.recordsTo(t, "error")) == 0 {
		t.Fatalf("the real error after a daemon restart must reach the parent")
	}
}

// Conductor ruling: a resume that goes straight to an error banner (waiting ->
// error, no running in between) reaches the parent at once, urgent, with what
// the pane shows, not 2 minutes later.
func TestIssue2792_BannerWithoutAFlipIsReportedAtOnce(t *testing.T) {
	t.Setenv("AGENTDECK_RESTART_BOOT_TIMEOUT", "1h")
	f := newTurnTestFixture(t)
	st := stubRestartPIDs(t)
	f.finishedTurn(t)
	st.restart(f, time.Now())
	st.substate = "auth-401"
	sends := *f.sends
	statuses := map[string]string{f.child.ID: "error", f.parent.ID: "waiting"}
	f.d.sweepRestartTokens("default", f.byID, statuses, time.Now())
	f.d.sweepRestartTokens("default", f.byID, statuses, time.Now()) // once only
	errs := f.recordsTo(t, "error")
	if len(errs) != 1 || !errs[0].IsUrgent() || !strings.Contains(errs[0].Text, "login or connection error") || *f.sends == sends {
		t.Fatalf("want one urgent record naming the banner, got %+v", f.inboxRecords(t))
	}
}

// The resumed Claude's SessionStart says waiting while its pane shows the
// login banner: not a clean resume, the error goes to the parent.
func TestIssue2792_BannerAtPromptIsNotABoot(t *testing.T) {
	f := newTurnTestFixture(t)
	st := stubRestartPIDs(t)
	f.finishedTurn(t)
	st.restart(f, time.Now())
	st.newHook("waiting", "SessionStart", time.Now())
	st.substate = "auth-401"
	f.appendTurn(t, fxResumeMeta())
	f.bootSeen()
	f.d.emitTurn("default", f.child, f.byID, "running", "waiting", time.Now().Add(3*time.Second), true)
	for _, r := range f.inboxRecords(t) {
		if strings.Contains(r.Text, "conversation resumed") {
			t.Fatalf("a pane showing a login error is not a clean resume: %+v", r)
		}
	}
	if errs := f.recordsTo(t, "error"); len(errs) != 1 || !strings.Contains(errs[0].Text, "login or connection error") {
		t.Fatalf("want the login error, got %+v", f.inboxRecords(t))
	}
}

// realRestart orders things as the real restart does (lander round 3): the
// token, respawn-pane -k (which makes the OLD Claude exit), the spawn record
// right after respawn returns, and the old Claude's async SessionEnd written
// hookLag after the respawn, stamped in whole seconds.
func (st *pidStub) realRestart(f *turnTestFixture, respawnAt time.Time, spawnRecordLag, hookLag time.Duration) {
	noteRestartToken(f.child, respawnAt.Add(-50*time.Millisecond))
	st.alive[st.pane] = false
	st.pane += 100
	st.alive[st.pane] = true
	markRestartSpawned(f.child, respawnAt.Add(spawnRecordLag))
	st.hook = &HookStatus{Status: "dead", Event: "SessionEnd", UpdatedAt: respawnAt.Add(hookLag).Truncate(time.Second)}
}

// Lander round 3: the old Claude's SessionEnd lands after the spawn record,
// across a second boundary. A clean idle restart must not page the parent.
func TestIssue2792_OldSessionEndAfterTheSpawnRecordIsHeld(t *testing.T) {
	for _, base := range []time.Duration{900 * time.Millisecond, 100 * time.Millisecond} { // across a boundary, and within one second
		for _, observed := range []bool{true, false} {
			t.Run(fmt.Sprintf("x.%03d/observed=%v", base.Milliseconds(), observed), func(t *testing.T) {
				f := newTurnTestFixture(t)
				st := stubRestartPIDs(t)
				f.finishedTurn(t)
				sends := *f.sends
				respawn := time.Now().Truncate(time.Second).Add(base)
				st.realRestart(f, respawn, 20*time.Millisecond, 150*time.Millisecond)
				f.d.emitTurn("default", f.child, f.byID, "running", "error", respawn.Add(time.Second), observed)
				for _, r := range f.inboxRecords(t) {
					if r.IsUrgent() {
						t.Fatalf("false urgent on a clean idle restart: %q", r.Text)
					}
				}
				if *f.sends != sends {
					t.Fatalf("no wake, sends %d -> %d", sends, *f.sends)
				}
			})
		}
	}
}

// Issue #2813: a real resumed Claude goes straight back to its prompt, so the
// daemon never sees a running -> waiting flip. The sweep finds the token holder
// back at its prompt (new process alive, its own SessionStart after the spawn,
// no new turn, no error banner) and, once the spawn is restartBootSettle old,
// sends the info line without waiting for the boot timeout. The old process's
// SessionEnd can land after the new SessionStart and overwrite it in the hook
// file; the SessionStart is then proven from the hook event history. Anything
// short of that stays silent.
func TestIssue2813_UnseenBootGetsTheInfoLine(t *testing.T) {
	t.Setenv("AGENTDECK_RESTART_BOOT_TIMEOUT", "1h")
	type fx struct {
		f      *turnTestFixture
		st     *pidStub
		at     time.Time
		status string
		after  time.Duration
	}
	history := func(t *testing.T, x *fx, events ...string) {
		t.Helper()
		var b strings.Builder
		for i, ev := range events {
			fmt.Fprintf(&b, "{\"at\":%q,\"event\":%q,\"status\":\"waiting\",\"pid\":%d}\n", x.at.Add(time.Duration(i+1)*100*time.Millisecond).UTC().Format(time.RFC3339Nano), ev, 500+i)
		}
		path := filepath.Join(filepath.Dir(hookStatusFilePath(x.f.child.ID)), x.f.child.ID+".events.jsonl")
		if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(b.String()), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	oldEndLast := func(t *testing.T, x *fx) { // the old SessionEnd landed after the new SessionStart
		x.st.hook = &HookStatus{Status: "dead", Event: "SessionEnd", UpdatedAt: x.at.Truncate(time.Second)}
		x.status = "error"
	}
	for _, c := range []struct {
		name  string
		setup func(t *testing.T, x *fx)
		want  bool
	}{
		{"back at its prompt", func(t *testing.T, x *fx) {}, true},
		{"old SessionEnd after the new SessionStart", func(t *testing.T, x *fx) {
			oldEndLast(t, x)
			history(t, x, "SessionStart", "SessionEnd")
		}, true},
		{"old SessionEnd last, no SessionStart in the history", func(t *testing.T, x *fx) {
			oldEndLast(t, x)
			history(t, x, "SessionEnd")
		}, false},
		{"old SessionEnd last, no history", func(t *testing.T, x *fx) { oldEndLast(t, x) }, false},
		{"the new process's own SessionEnd", func(t *testing.T, x *fx) {
			x.st.hook = &HookStatus{Status: "dead", Event: "SessionEnd", UpdatedAt: x.at.Add(3 * time.Second).Truncate(time.Second)}
			x.status = "error"
			history(t, x, "SessionStart", "SessionEnd")
		}, false},
		{"too soon after the spawn", func(t *testing.T, x *fx) { x.after = time.Second }, false},
		{"no SessionStart yet", func(t *testing.T, x *fx) {
			x.st.hook = &HookStatus{Status: "dead", Event: "SessionEnd", UpdatedAt: x.at.Truncate(time.Second)}
		}, false},
		{"SessionStart from before the restart", func(t *testing.T, x *fx) {
			x.st.hook = &HookStatus{Status: "waiting", Event: "SessionStart", UpdatedAt: x.at.Add(-time.Hour)}
		}, false},
		{"new process dead", func(t *testing.T, x *fx) { x.st.alive[x.st.pane] = false }, false},
		{"new turn in the transcript", func(t *testing.T, x *fx) {
			x.f.appendTurn(t, fxHuman("u2", "next task"))
		}, false},
	} {
		t.Run(c.name, func(t *testing.T) {
			f := newTurnTestFixture(t)
			st := stubRestartPIDs(t)
			n := f.finishedTurn(t)
			sends := *f.sends
			f.child.SetRestartReason(RestartReasonIdleBuildRefresh)
			at := time.Now()
			st.restart(f, at)
			f.d.noteRestartBuild(f.child.ID, "/opt/claude/versions/2.1.303")
			st.newHook("waiting", "SessionStart", at)
			f.appendTurn(t, fxResumeMeta())
			x := &fx{f: f, st: st, at: at, status: "waiting", after: 6 * time.Second} // past restartBootSettle
			c.setup(t, x)
			statuses := map[string]string{f.child.ID: x.status, f.parent.ID: "waiting"}
			f.d.sweepRestartTokens("default", f.byID, statuses, at.Add(x.after))
			f.d.sweepRestartTokens("default", f.byID, statuses, at.Add(x.after+time.Second)) // once only

			var lines []TransitionNotificationEvent
			for _, r := range f.inboxRecords(t)[n:] {
				if r.IsUrgent() {
					t.Fatalf("an idle restart is never urgent: %+v", r)
				}
				if strings.Contains(r.Text, "conversation resumed") {
					lines = append(lines, r)
				}
			}
			_, left := readRestartToken(f.child.ID)
			if c.want {
				if len(lines) != 1 || lines[0].IsUrgent() || lines[0].ToStatus == string(StatusError) || !strings.Contains(lines[0].Text, "Refreshed onto claude 2.1.303 while idle") {
					t.Fatalf("want exactly the one refresh info line, got %+v", f.inboxRecords(t)[n:])
				}
				if left {
					t.Fatalf("the info line spends the token")
				}
				if *f.sends != sends {
					t.Fatalf("no wake, sends %d -> %d", sends, *f.sends)
				}
				return
			}
			if len(lines) != 0 {
				t.Fatalf("not provably a clean resume, no info line: %+v", lines)
			}
			if !left {
				t.Fatalf("no info line and before the boot timeout: the token is kept")
			}
		})
	}
}
