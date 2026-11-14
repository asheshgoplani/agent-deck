package session

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// Field case (rc.3): an idle build refresh restarts a child whose transcript
// tail is an unanswered user record (a <task-notification>). The #2813 sweep
// sends the info line and spends the token; ~10 s later, past the flush-race
// window, the same idle prompt is an unclassified running -> waiting flip
// with no token left, which reached the parent as a bare "finished" record.
// While the info line's banner watch is live and the transcript has no new
// turn, that flip is the same resume, not news.

// restartEchoSetup restarts the child after a finished turn and an
// unanswered task notification, and runs the sweep that sends the info line.
// The restart is a minute in the past so the transcript can be dated after it
// and still past the flush-race window.
func restartEchoSetup(t *testing.T) (*turnTestFixture, *pidStub, time.Time, int) {
	return restartEchoSetupWith(t, true)
}

// restartEchoSetupWith: resumeWrites says whether the resumed process appends
// its boot records to the transcript the instance resolves.
func restartEchoSetupWith(t *testing.T, resumeWrites bool) (*turnTestFixture, *pidStub, time.Time, int) {
	t.Helper()
	t.Setenv("AGENTDECK_RESTART_BOOT_TIMEOUT", "2m")
	f := newTurnTestFixture(t)
	st := stubRestartPIDs(t)
	n := f.finishedTurn(t)
	at := time.Now().Add(-time.Minute)
	f.appendTurn(t, fxTaskNotification("u-task"))
	restartEchoSettle(t, f, at.Add(-10*time.Second)) // unanswered long enough to count as no turn in flight
	f.child.SetRestartReason(RestartReasonIdleBuildRefresh)
	st.restart(f, at)
	f.d.noteRestartBuild(f.child.ID, "/opt/claude/versions/2.1.303")
	st.newHook("waiting", "SessionStart", at)
	if resumeWrites {
		f.appendTurn(t, fxResumeMeta(), `{"type":"last-prompt"}`)
		restartEchoSettle(t, f, at.Add(2*time.Second))
	}
	statuses := map[string]string{f.child.ID: "waiting", f.parent.ID: "waiting"}
	f.d.sweepRestartTokens("default", f.byID, statuses, at.Add(6*time.Second))
	added := f.inboxRecords(t)[n:]
	if len(added) != 1 || !strings.Contains(added[0].Text, "while idle; conversation resumed, no new turn.") {
		t.Fatalf("precondition: the info line, got %+v", added)
	}
	return f, st, at, n + 1
}

// restartEchoSettle dates the transcript's last write at when.
func restartEchoSettle(t *testing.T, f *turnTestFixture, when time.Time) {
	t.Helper()
	if err := os.Chtimes(f.transcript, when, when); err != nil {
		t.Fatalf("chtimes: %v", err)
	}
}

func TestRestartResumeEcho_UnansweredTailGetsNoSecondRecord(t *testing.T) {
	for _, observed := range []bool{true, false} {
		f, _, at, n := restartEchoSetup(t)
		sends := *f.sends
		before, _ := ReadInboxStats(f.parent.ID)
		f.d.emitTurn("default", f.child, f.byID, "running", "waiting", at.Add(16*time.Second), observed)
		f.d.emitTurn("default", f.child, f.byID, "waiting", "idle", at.Add(20*time.Second), observed)
		if added := f.inboxRecords(t)[n:]; len(added) != 0 {
			t.Fatalf("observed=%v: the resume was already reported; want no second record, got %+v", observed, added)
		}
		if after, _ := ReadInboxStats(f.parent.ID); after.NoiseSuppressed != before.NoiseSuppressed+2 {
			t.Fatalf("observed=%v: both echoes count as noise, NoiseSuppressed %d -> %d", observed, before.NoiseSuppressed, after.NoiseSuppressed)
		}
		if *f.sends != sends {
			t.Fatalf("observed=%v: no wake, sends %d -> %d", observed, sends, *f.sends)
		}
	}
}

// A real turn inside the watch window still reaches the parent.
func TestRestartResumeEcho_RealTurnInsideTheWatchNotifies(t *testing.T) {
	t.Run("assistant reply", func(t *testing.T) {
		f, _, at, n := restartEchoSetup(t)
		f.appendTurn(t, fxAssistantText("a2", "Task done; results in /tmp/out."))
		restartEchoSettle(t, f, at.Add(20*time.Second))
		f.d.emitTurn("default", f.child, f.byID, "running", "waiting", at.Add(30*time.Second), true)
		added := f.inboxRecords(t)[n:]
		if len(added) != 1 || added[0].Text != "Task done; results in /tmp/out." {
			t.Fatalf("want the new turn, got %+v", added)
		}
	})
	t.Run("new prompt with no reply text", func(t *testing.T) {
		f, _, at, n := restartEchoSetup(t)
		f.appendTurn(t, fxHuman("u3", "next task"))
		restartEchoSettle(t, f, at.Add(20*time.Second))
		f.d.emitTurn("default", f.child, f.byID, "running", "waiting", at.Add(30*time.Second), true)
		if added := f.inboxRecords(t)[n:]; len(added) != 1 {
			t.Fatalf("a new user record after the restart is a turn: want one record, got %+v", added)
		}
	})
}

// An error after the info line still pages.
func TestRestartResumeEcho_ErrorAfterInfoLinePages(t *testing.T) {
	f, _, at, n := restartEchoSetup(t)
	f.d.emitTurn("default", f.child, f.byID, "running", "error", at.Add(16*time.Second), true)
	added := f.inboxRecords(t)[n:]
	if len(added) != 1 || !added[0].IsUrgent() {
		t.Fatalf("want one urgent error record, got %+v", added)
	}
}

// A permission request or open menu after the resume blocks the child on
// input: no new transcript turn, but the parent must hear it.
func TestRestartResumeEcho_PermissionWaitNotifies(t *testing.T) {
	for _, c := range []struct {
		name  string
		setup func(st *pidStub, at time.Time)
	}{
		{"PermissionRequest hook", func(st *pidStub, at time.Time) { st.newHook("waiting", "PermissionRequest", at.Add(10*time.Second)) }},
		{"elicitation Notification hook", func(st *pidStub, at time.Time) { st.newHook("waiting", "Notification", at.Add(10*time.Second)) }},
		{"menu on the pane", func(st *pidStub, at time.Time) { st.substate = "interactive-menu" }},
	} {
		t.Run(c.name, func(t *testing.T) {
			f, st, at, n := restartEchoSetup(t)
			c.setup(st, at)
			f.d.emitTurn("default", f.child, f.byID, "running", "waiting", at.Add(16*time.Second), true)
			if added := f.inboxRecords(t)[n:]; len(added) != 1 {
				t.Fatalf("want the permission wait reported, got %+v", added)
			}
		})
	}
}

// Past the boot timeout the watch is over: the flip is reported as before.
func TestRestartResumeEcho_AfterTheWatchExpiresReports(t *testing.T) {
	f, _, at, n := restartEchoSetup(t)
	f.d.emitTurn("default", f.child, f.byID, "running", "waiting", at.Add(3*time.Minute), true)
	if added := f.inboxRecords(t)[n:]; len(added) != 1 {
		t.Fatalf("want one record after the watch, got %+v", added)
	}
}

// #2184 style: the resumed process writes a session file the instance does
// not resolve, so the old transcript never changes. A flip the daemon saw is
// then news: noTurnSince on the old file cannot see the new turn.
func TestRestartResumeEcho_StaleTranscriptNotifies(t *testing.T) {
	t.Run("observed flip, old transcript untouched since the restart", func(t *testing.T) {
		f, _, at, n := restartEchoSetupWith(t, false)
		f.d.emitTurn("default", f.child, f.byID, "running", "waiting", at.Add(16*time.Second), true)
		if added := f.inboxRecords(t)[n:]; len(added) != 1 {
			t.Fatalf("want the observed flip reported, got %+v", added)
		}
	})
	t.Run("resolved transcript is not the token's", func(t *testing.T) {
		f, _, at, n := restartEchoSetup(t)
		f.child.ClaudeSessionID = "99999999-2222-3333-4444-555555555555"
		path := filepath.Join(filepath.Dir(f.transcript), f.child.ClaudeSessionID+".jsonl")
		if err := os.WriteFile(path, []byte(fxHuman("u9", "new session prompt")+"\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		old := at.Add(20 * time.Second)
		_ = os.Chtimes(path, old, old)
		if got := f.child.GetJSONLPath(); got != path {
			t.Fatalf("precondition: resolves the new file, got %q", got)
		}
		f.d.emitTurn("default", f.child, f.byID, "running", "waiting", at.Add(30*time.Second), false)
		if added := f.inboxRecords(t)[n:]; len(added) != 1 {
			t.Fatalf("want the flip on the new transcript reported, got %+v", added)
		}
	})
}

// A second restart's news is not swallowed by the first restart's watch,
// even before sweepRestartBanners has run: here restart 2's process is dead
// at its prompt.
func TestRestartResumeEcho_StaleWatchDoesNotDropTheNextRestartsNews(t *testing.T) {
	f, st, at, n := restartEchoSetup(t)
	at2 := at.Add(30 * time.Second)
	st.restart(f, at2)
	st.alive[st.pane] = false
	f.d.emitTurn("default", f.child, f.byID, "running", "waiting", at2.Add(10*time.Second), false)
	if added := f.inboxRecords(t)[n:]; len(added) != 1 {
		t.Fatalf("want restart 2's flip reported, got %+v", added)
	}
}

// A partial trailing line is not provably a boot.
func TestRestartResumeEcho_PartialTrailingLineNotifies(t *testing.T) {
	f, _, at, n := restartEchoSetup(t)
	f.appendTurn(t, `{"type":"assistant","uuid":"a3","message":{"role":"assis`)
	restartEchoSettle(t, f, at.Add(20*time.Second))
	f.d.emitTurn("default", f.child, f.byID, "running", "waiting", at.Add(30*time.Second), false)
	if added := f.inboxRecords(t)[n:]; len(added) != 1 {
		t.Fatalf("want the flip reported, got %+v", added)
	}
}
