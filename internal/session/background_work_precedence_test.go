package session

import (
	"fmt"
	"os"
	"path/filepath"
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
	body := fmt.Sprintf(`{"status":"waiting","session_id":"sess-610","event":%q,"ts":%d}`,
		event, time.Now().Add(-time.Second).Unix())
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

// Hook event alone: a PermissionRequest is never held running even when the
// frame does not (yet) show the dialog.
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
