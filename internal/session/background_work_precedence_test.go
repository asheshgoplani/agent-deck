package session

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// Issue #2473 review round 2: background work never outranks an open menu or
// an error. A child blocked on a permission prompt / question while its
// workflow runs is waiting (its parent must answer), and a session whose
// credentials died is not green. Main (v1.16.24) reads waiting for both; the
// first cut of #2473 read running/interactive-menu and running/auth-401.

// writeHookWaitingEvent writes a fresh waiting hook record for event, bound to
// the same hook session (sess-610) the other hook-lag fixtures use.
func writeHookWaitingEvent(t *testing.T, instanceID, event string) {
	t.Helper()
	writeHookWaitingEventAged(t, instanceID, event, time.Second)
}

// writeHookWaitingEventAged is writeHookWaitingEvent for an event age old.
func writeHookWaitingEventAged(t *testing.T, instanceID, event string, age time.Duration) {
	t.Helper()
	body := fmt.Sprintf(`{"status":"waiting","session_id":"sess-610","event":%q,"ts":%d}`,
		event, time.Now().Add(-age).Unix())
	if err := os.WriteFile(filepath.Join(GetHooksDir(), instanceID+".json"), []byte(body), 0o644); err != nil {
		t.Fatalf("write hook: %v", err)
	}
}

// pendingWorkflowTranscript writes the probe transcript up to the pending
// workflow launch (probe-two-agents in flight, launched 20s ago).
func pendingWorkflowTranscript(t *testing.T, inst *Instance) {
	t.Helper()
	inst.ClaudeSessionID = "sess-610"
	writeInstanceTranscript(t, inst, restamp(loadProbeTranscript(t)[:10], time.Now().Add(-20*time.Second)))
}

// reloadAs is a fresh CLI process (list --json, session show) loading inst's
// row with the given persisted status.
func reloadAs(t *testing.T, profile string, inst *Instance, status Status) *Instance {
	t.Helper()
	storage, err := NewStorageWithProfile(profile)
	if err != nil {
		t.Fatalf("storage: %v", err)
	}
	t.Cleanup(func() { storage.Close() })
	return persistAndReload(t, storage, inst, status)
}

// Hook fast path: every waiting hook event over a menu frame with a workflow
// row AND a pending transcript workflow stays waiting with the menu substate.
func TestBackgroundWork2473_MenuOutranksWorkOnHookPath(t *testing.T) {
	cases := []struct {
		name, fixture, event string
	}{
		{"PermissionRequest over permission dialog", "workflow-permission-menu.txt", "PermissionRequest"},
		{"Notification over AskUserQuestion", "workflow-ask-question.txt", "Notification"},
		// The frame alone blocks it too, whatever the event says.
		{"Stop over permission dialog", "workflow-permission-menu.txt", "Stop"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			t.Setenv("CLAUDE_CONFIG_DIR", "")
			turnFacts = &turnFactsCache{entries: map[string]turnFactsCacheEntry{}}
			inst, cleanup := startHookLagInstance(t, "bg-menu", loadPaneFixture(t, c.fixture))
			defer cleanup()
			pendingWorkflowTranscript(t, inst)
			writeHookWaitingEvent(t, inst.ID, c.event)

			status, sub := cliPass(t, inst)
			if status != StatusWaiting || sub != SubstateInteractiveMenu {
				t.Fatalf("%s + menu + pending workflow = %q/%q (detail %q), want waiting/interactive-menu",
					c.event, status, sub, inst.SubstateDetail())
			}
			if work := inst.BackgroundWork(); work.InFlight() {
				t.Fatalf("background work reported on a blocked child: %+v", work)
			}
		})
	}
}

// Hook event alone: a fresh PermissionRequest (inside blockingHookGrace) is
// not held running even when the frame does not show the dialog yet: the
// synchronous hook fires just before Claude draws it.
func TestBackgroundWork2473_PermissionRequestEventIsNeverHeld(t *testing.T) {
	t.Setenv("CLAUDE_CONFIG_DIR", "")
	turnFacts = &turnFactsCache{entries: map[string]turnFactsCacheEntry{}}
	inst, cleanup := startHookLagInstance(t, "bg-perm-event", loadPaneFixture(t, "workflow-running.txt"))
	defer cleanup()
	pendingWorkflowTranscript(t, inst)
	writeHookWaitingEvent(t, inst.ID, "PermissionRequest")
	if status, _ := cliPass(t, inst); status != StatusWaiting {
		t.Fatalf("PermissionRequest + workflow row = %q, want waiting", status)
	}
	// The Stop hook over the same frame is still running (the #2473 rule).
	writeHookWaitingEvent(t, inst.ID, "Stop")
	if status, sub := cliPass(t, inst); status != StatusRunning || sub != SubstateBackgroundWork {
		t.Fatalf("Stop + workflow row = %q/%q, want running/background-work", status, sub)
	}
}

// tmux path (no fresh hook): the menu frame with a pending transcript workflow
// is waiting/interactive-menu, not promoted by the instance merge.
func TestBackgroundWork2473_MenuOutranksWorkOnTmuxPath(t *testing.T) {
	t.Setenv("CLAUDE_CONFIG_DIR", "")
	turnFacts = &turnFactsCache{entries: map[string]turnFactsCacheEntry{}}
	inst, cleanup := startPaneInstance(t, "claude", "claude-bgmenu-2473", loadPaneFixture(t, "workflow-permission-menu.txt"))
	defer cleanup()
	pendingWorkflowTranscript(t, inst)
	fresh := reloadAs(t, "_test-2473-bgmenu", inst, StatusWaiting)
	status, sub := cliPass(t, fresh)
	if status != StatusWaiting || sub != SubstateInteractiveMenu {
		t.Fatalf("menu + pending workflow (tmux path) = %q/%q (detail %q), want waiting/interactive-menu",
			status, sub, fresh.SubstateDetail())
	}
}

// auth-401 banner with the workflow row: error on the tmux path, and on the
// Stop-hook path the same waiting/auth-401 as main, never running.
func TestBackgroundWork2473_AuthErrorOutranksWork(t *testing.T) {
	t.Run("tmux path", func(t *testing.T) {
		t.Setenv("CLAUDE_CONFIG_DIR", "")
		turnFacts = &turnFactsCache{entries: map[string]turnFactsCacheEntry{}}
		inst, cleanup := startPaneInstance(t, "claude", "claude-bg401-2473", loadPaneFixture(t, "workflow-auth-401.txt"))
		defer cleanup()
		pendingWorkflowTranscript(t, inst)
		fresh := reloadAs(t, "_test-2473-bg401", inst, StatusRunning)
		if status, sub := cliPass(t, fresh); status != StatusError || sub != SubstateAuth401 {
			t.Fatalf("401 + workflow (tmux path) = %q/%q, want error/auth-401", status, sub)
		}
	})
	t.Run("Stop hook", func(t *testing.T) {
		t.Setenv("CLAUDE_CONFIG_DIR", "")
		turnFacts = &turnFactsCache{entries: map[string]turnFactsCacheEntry{}}
		inst, cleanup := startHookLagInstance(t, "bg-401", loadPaneFixture(t, "workflow-auth-401.txt"))
		defer cleanup()
		pendingWorkflowTranscript(t, inst)
		writeHookWaitingEvent(t, inst.ID, "Stop")
		status, sub := cliPass(t, inst)
		if status == StatusRunning || sub != SubstateAuth401 {
			t.Fatalf("401 + workflow (Stop hook) = %q/%q, want waiting/auth-401 (never running)", status, sub)
		}
		if work := inst.BackgroundWork(); work.InFlight() {
			t.Fatalf("background work reported on a dead-credential session: %+v", work)
		}
	})
}

// The daemon: a permission request that arrives while the workflow runs is
// written to the parent's inbox (one waiting record), even if the row the
// daemon reads still says running. A Stop in the same state is held.
func TestBackgroundWork2473_DaemonEmitsPermissionRequestWhileWorkflowRuns(t *testing.T) {
	f := newTurnTestFixture(t)
	f.appendTurn(t, fxHuman("u0", "run the follow-on workflow"))
	f.appendTurn(t, fxWorkflowLaunch("wqphbmkuj", "comms-followon-round3")...)
	f.appendTurn(t, fxAssistantText("a0", "Launched comms-followon-round3; pushing the branch next."), fxTurnDuration(1))

	running := map[string]string{f.child.ID: "running", f.parent.ID: "waiting"}
	stop := map[string]hookTransitionCandidate{f.child.ID: {ToStatus: "waiting", Timestamp: time.Now(), Event: "Stop"}}
	f.d.emitHookTransitionCandidates("default", f.byID, running, running, stop)
	if got := f.inboxRecords(t); len(got) != 0 {
		t.Fatalf("Stop while the workflow runs wrote records: %+v", got)
	}

	perm := map[string]hookTransitionCandidate{f.child.ID: {ToStatus: "waiting", Timestamp: time.Now(), Event: "PermissionRequest"}}
	f.d.emitHookTransitionCandidates("default", f.byID, running, running, perm)
	got := f.inboxRecords(t)
	if len(got) != 1 || got[0].ToStatus != "waiting" {
		t.Fatalf("permission request while the workflow runs = %+v, want one waiting record", got)
	}
}

func TestHookEventBlocksTurn(t *testing.T) {
	for event, want := range map[string]bool{
		"PermissionRequest": true, "permissionrequest": true, "Notification": true,
		"Stop": false, "": false, "UserPromptSubmit": false,
	} {
		if got := hookEventBlocksTurn(event); got != want {
			t.Errorf("hookEventBlocksTurn(%q) = %v, want %v", event, got, want)
		}
	}
}

// Issue #2473 review round 3: a launch turn that ends with a prose question
// ("Would you like me to ...?") opens no menu. The workflow is in flight, so
// both the Stop-hook path and the tmux path read running/background-work.
func TestBackgroundWork2473_ProseQuestionKeepsWorkflowRunning(t *testing.T) {
	would := loadPaneFixture(t, "workflow-prose-question.txt")
	frames := map[string]string{
		"would-you-like": would,
		"do-you-want": strings.Replace(would, "Would you like me to run the test suite while it finishes?",
			"Do you want me to open a PR once both agents report back?", 1),
	}
	for name, frame := range frames {
		t.Run(name+"/Stop hook", func(t *testing.T) {
			t.Setenv("CLAUDE_CONFIG_DIR", "")
			turnFacts = &turnFactsCache{entries: map[string]turnFactsCacheEntry{}}
			inst, cleanup := startHookLagInstance(t, "bg-prose", frame)
			defer cleanup()
			pendingWorkflowTranscript(t, inst)
			writeHookWaitingEvent(t, inst.ID, "Stop")
			status, sub := cliPass(t, inst)
			if status != StatusRunning || sub != SubstateBackgroundWork {
				t.Fatalf("prose question + workflow (Stop hook) = %q/%q (detail %q), want running/background-work",
					status, sub, inst.SubstateDetail())
			}
			if got := inst.SubstateDetail(); got != "workflow probe-two-agents 1/2 · 22s" {
				t.Fatalf("substate detail = %q", got)
			}
		})
		t.Run(name+"/tmux path", func(t *testing.T) {
			t.Setenv("CLAUDE_CONFIG_DIR", "")
			turnFacts = &turnFactsCache{entries: map[string]turnFactsCacheEntry{}}
			inst, cleanup := startPaneInstance(t, "claude", "claude-bgprose-2473", frame)
			defer cleanup()
			pendingWorkflowTranscript(t, inst)
			fresh := reloadAs(t, "_test-2473-bgprose", inst, StatusWaiting)
			if status, sub := cliPass(t, fresh); status != StatusRunning || sub != SubstateBackgroundWork {
				t.Fatalf("prose question + workflow (tmux path) = %q/%q (detail %q), want running/background-work",
					status, sub, fresh.SubstateDetail())
			}
		})
	}
}

// The daemon over the same prose-question frame: the launch turn's Stop is
// held while the workflow runs, so no inbox record is written (round 2 wrote
// a premature running -> waiting record because the frame read as a menu).
func TestBackgroundWork2473_DaemonWritesNoRecordUnderProseQuestion(t *testing.T) {
	t.Setenv("CLAUDE_CONFIG_DIR", "")
	turnFacts = &turnFactsCache{entries: map[string]turnFactsCacheEntry{}}
	pane, cleanup := startHookLagInstance(t, "bg-prose-daemon", loadPaneFixture(t, "workflow-prose-question.txt"))
	defer cleanup()
	pendingWorkflowTranscript(t, pane)
	writeHookWaitingEvent(t, pane.ID, "Stop")
	if status, _ := cliPass(t, pane); status != StatusRunning {
		t.Errorf("prose question + workflow = %q, want running", status)
	}

	// The daemon's child row reads this pane (cached frame verdicts only; the
	// daemon never captures) and its own transcript with the launch pending.
	f := newTurnTestFixture(t)
	f.child.tmuxSession = pane.tmuxSession
	f.appendTurn(t, fxHuman("u0", "run the follow-on workflow"))
	f.appendTurn(t, fxWorkflowLaunch("wqphbmkuj", "comms-followon-round3")...)
	f.appendTurn(t, fxAssistantText("a0", "Launched comms-followon-round3. Would you like me to run the test suite while it finishes?"), fxTurnDuration(1))

	running := map[string]string{f.child.ID: "running", f.parent.ID: "waiting"}
	stop := map[string]hookTransitionCandidate{f.child.ID: {ToStatus: "waiting", Timestamp: time.Now(), Event: "Stop"}}
	for i := 0; i < 5; i++ {
		f.d.recordTerminalTurns("default", f.byID, running, nil)
		f.d.emitHookTransitionCandidates("default", f.byID, running, running, stop)
	}
	if got := f.inboxRecords(t); len(got) != 0 {
		t.Fatalf("records written while the workflow runs under a prose question: %+v", got)
	}
}

// Issue #2473 review round 3: a blocking hook event older than
// blockingHookGrace no longer decides on its own. Claude fires no hook when
// a permission dialog is dismissed with Esc, so the file keeps saying
// waiting/PermissionRequest while the workflow runs and no menu is drawn: the
// frame decides. With the menu still open it stays waiting.
func TestBackgroundWork2473_StaleBlockingHookFollowsTheFrame(t *testing.T) {
	escaped := strings.Replace(loadPaneFixture(t, "workflow-running.txt"),
		"✻ Waiting for 1 dynamic workflow to finish",
		"⏺ Bash(git push origin main)\n  ⎿  Interrupted · What should Claude do instead?", 1)
	cases := []struct {
		name, frame, event string
		status             Status
		sub                Substate
	}{
		{"PermissionRequest, dialog dismissed", escaped, "PermissionRequest", StatusRunning, SubstateBackgroundWork},
		{"Notification, dialog dismissed", escaped, "Notification", StatusRunning, SubstateBackgroundWork},
		{"PermissionRequest, dialog still open", loadPaneFixture(t, "workflow-permission-menu.txt"), "PermissionRequest", StatusWaiting, SubstateInteractiveMenu},
		{"Notification, question still open", loadPaneFixture(t, "workflow-ask-question.txt"), "Notification", StatusWaiting, SubstateInteractiveMenu},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			t.Setenv("CLAUDE_CONFIG_DIR", "")
			turnFacts = &turnFactsCache{entries: map[string]turnFactsCacheEntry{}}
			inst, cleanup := startHookLagInstance(t, "bg-stale-block", c.frame)
			defer cleanup()
			pendingWorkflowTranscript(t, inst)
			writeHookWaitingEventAged(t, inst.ID, c.event, 20*time.Second)
			if status, sub := cliPass(t, inst); status != c.status || sub != c.sub {
				t.Fatalf("%s 20s old = %q/%q (detail %q), want %q/%q",
					c.event, status, sub, inst.SubstateDetail(), c.status, c.sub)
			}
		})
	}
}

func TestBlockingHookInGrace(t *testing.T) {
	now := time.Now()
	cases := []struct {
		event string
		age   time.Duration
		want  bool
	}{
		{"PermissionRequest", time.Second, true},
		{"Notification", blockingHookGrace - time.Second, true},
		{"PermissionRequest", blockingHookGrace, false},
		{"PermissionRequest", 20 * time.Second, false},
		{"Stop", time.Second, false},
	}
	for _, c := range cases {
		if got := blockingHookInGrace(c.event, now.Add(-c.age), now); got != c.want {
			t.Errorf("blockingHookInGrace(%q, %s old) = %v, want %v", c.event, c.age, got, c.want)
		}
	}
}
