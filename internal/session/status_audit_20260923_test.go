package session

import (
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// Status-detection audit 2026-09-23. Each test renders a REAL captured pane
// shape (see internal/tmux/testdata/status_corpus) in a live tmux pane and
// drives the fresh-process status pass a remote's `list --json`, the notify
// daemon and the TUI all go through. Before the audit every one of these
// reported the wrong colour.

// startPaneInstance is startCodexPaneInstance for any tool.
func startPaneInstance(t *testing.T, tool, name, frame string) (*Instance, func()) {
	t.Helper()
	skipIfNoTmuxBinary(t)
	tmpHome := t.TempDir()
	t.Setenv("HOME", tmpHome)
	t.Setenv("CODEX_HOME", filepath.Join(tmpHome, ".codex"))
	panePath := filepath.Join(tmpHome, "pane.txt")
	if err := os.WriteFile(panePath, []byte(frame), 0o644); err != nil {
		t.Fatal(err)
	}
	inst := NewInstanceWithTool("audit-"+name, tmpHome, tool)
	inst.Command = tool
	inst.tmuxSession.Command = tool
	if err := inst.tmuxSession.Start(fmt.Sprintf("sh -c 'cat %q; exec sleep 3600'", panePath)); err != nil {
		t.Fatalf("tmux start: %v", err)
	}
	cleanup := func() { _ = inst.tmuxSession.Kill() }
	time.Sleep(2 * time.Second)
	return inst, cleanup
}

// Codex draws its live status line ABOVE the composer and the model/context
// footer. This is the exact tail of a running gpt-6 Codex session on the
// audit night; the frame layer read it as waiting (the "esc to interrupt"
// string is gated to the last 3 lines), so whenever the pane-title spinner
// was not fresh the light flipped yellow mid-turn and fired a transition.
const codexWorkingAboveComposer = "    224  func openTestBus(t *testing.T) *Bus {\n" +
	"\n" +
	"• Working (9m 41s • esc to interrupt)\n" +
	"\n" +
	"› Ask Codex to do anything\n" +
	"\n" +
	"  gpt-6-sol · /private/tmp/exec-core-slice4-r2 · Context 64% left · Context 36% used · weekly 94% left · 258K window\n"

func TestAudit_CodexWorkingAboveComposerIsRunning(t *testing.T) {
	inst, cleanup := startPaneInstance(t, "codex", "codex-working", codexWorkingAboveComposer)
	defer cleanup()
	storage, err := NewStorageWithProfile("_test-audit-codex-working")
	if err != nil {
		t.Fatalf("storage: %v", err)
	}
	defer storage.Close()
	// A fresh process that loaded the row as waiting (what the remote agent
	// probe and `list --json` start from) must still see the work.
	fresh := persistAndReload(t, storage, inst, StatusWaiting)
	if status, _ := cliPass(t, fresh); status != StatusRunning {
		t.Fatalf("fresh process = %q, want running: the pane shows • Working (… esc to interrupt) above the composer", status)
	}
}

// A Claude turn that finished (completion line, DONE sentinel, idle prompt)
// with run_in_background shells still alive. On the audit night this was
// every "running" row on one remote host, hours after the workers had
// finished; the operator could not tell where to act.
const claudeDoneWithBackgroundShells = "  ===AGENTDECK_DONE=== status=ok summary=BUG-72 fixed\n" +
	"\n" +
	"✻ Crunched for 1h 50m 16s · done 9:17 PM · 1 shell still running\n" +
	"\n" +
	"────────────────────────────────────────────────────────────\n" +
	"❯ \n" +
	"────────────────────────────────────────────────────────────\n" +
	"\n" +
	"  ⏵⏵ bypass permissions on · PR #508 · 1 shell · ← for agents\n"

func TestAudit_ClaudeBackgroundShellsAtPromptIsWaiting(t *testing.T) {
	inst, cleanup := startPaneInstance(t, "claude", "claude-bgshell", claudeDoneWithBackgroundShells)
	defer cleanup()
	storage, err := NewStorageWithProfile("_test-audit-claude-bgshell")
	if err != nil {
		t.Fatalf("storage: %v", err)
	}
	defer storage.Close()
	fresh := persistAndReload(t, storage, inst, StatusRunning)
	status, sub := cliPass(t, fresh)
	if status != StatusWaiting {
		t.Fatalf("fresh process = %q, want waiting: the turn is done and the prompt is idle", status)
	}
	if sub != SubstateBackgroundWork {
		t.Fatalf("substate = %q, want %q", sub, SubstateBackgroundWork)
	}
}

// Same session shape, but the turn ended awaiting a background AGENT: Claude
// resumes on its own, so this one stays running (the intent of #1544 kept
// for the case it was actually about).
const claudeAwaitingBackgroundAgent = "⏺ Probe launched. Ending my turn.\n" +
	"\n" +
	"✻ Waiting for 1 background agent to finish\n" +
	"\n" +
	"────────────────────────────────────────────────────────────\n" +
	"❯ \n" +
	"────────────────────────────────────────────────────────────\n" +
	"  ⏵⏵ bypass permissions on · 1 shell · ← for agents\n"

func TestAudit_ClaudeAwaitingBackgroundAgentStaysRunning(t *testing.T) {
	inst, cleanup := startPaneInstance(t, "claude", "claude-bgagent", claudeAwaitingBackgroundAgent)
	defer cleanup()
	if status, _ := cliPass(t, inst); status != StatusRunning {
		t.Fatalf("status = %q, want running while a background agent is awaited", status)
	}
}

// A Claude session driving many sub-agents: the roster is drawn under the
// footer, one row per agent, and pushes the spinner line out of every
// bottom-anchored detector window. 18 rows here; the real session had 3 and
// was already at the edge of the prompt detector's 10-line spinner guard.
func claudeSpinnerWithAgentRoster(rows int) string {
	frame := "  ⎿  $ tmux capture-pane -p …\n" +
		"\n" +
		"✳ Moonwalking… (2m 11s · ↓ 3.8k tokens)\n" +
		"  ⎿  Tip: Use /btw to ask a quick side question without interrupting Claude's current work\n" +
		"\n" +
		"────────────────────────────────────────────────────────────\n" +
		"❯ \n" +
		"────────────────────────────────────────────────────────────\n" +
		"  [personal] u@host:/private/tmp/exec-status-audit/src | [Fable 5.1] ctx:9% in:92.7k out:2\n" +
		"  ⏵⏵ bypass permissions on (shift+tab to cycle) · ← for agents\n" +
		"  ⏺ main\n"
	for i := 0; i < rows; i++ {
		frame += fmt.Sprintf("  ◯ general-purpose  Task %d\n", i+1)
	}
	return frame
}

func TestAudit_ClaudeSubAgentRosterDoesNotHideSpinner(t *testing.T) {
	inst, cleanup := startPaneInstance(t, "claude", "claude-roster", claudeSpinnerWithAgentRoster(18))
	defer cleanup()
	storage, err := NewStorageWithProfile("_test-audit-claude-roster")
	if err != nil {
		t.Fatalf("storage: %v", err)
	}
	defer storage.Close()
	fresh := persistAndReload(t, storage, inst, StatusWaiting)
	if status, _ := cliPass(t, fresh); status != StatusRunning {
		t.Fatalf("fresh process = %q, want running: the spinner line is live above the agent roster", status)
	}
}

// The notify daemon without a live TUI reloads every Instance per pass, so
// the running→waiting debounce never held: one misread frame became a real
// transition event. With the carried prior the first flip sample is held
// and the second confirms, exactly like the TUI's long-lived instances.
func TestAudit_DaemonCarriesFlipDebounceAcrossPasses(t *testing.T) {
	inboxTestHome(t)
	profile := "_test-audit-daemon-debounce"
	storage, err := NewStorageWithProfile(profile)
	if err != nil {
		t.Fatalf("storage: %v", err)
	}
	defer storage.Close()
	inst := &Instance{
		ID: "audit-debounce", Title: "worker", ProjectPath: t.TempDir(), GroupPath: DefaultGroupPath,
		Tool: "codex", Status: StatusRunning, CreatedAt: time.Now(),
	}
	if err := storage.SaveWithGroups([]*Instance{inst}, nil); err != nil {
		t.Fatalf("save: %v", err)
	}

	// The probe stands in for updateStatus's tmux path: one pane sample per
	// pass, run through the real debounce with the instance's carried state.
	sample := StatusRunning
	orig := updateInstanceStatus.Load()
	defer updateInstanceStatus.Store(orig)
	updateInstanceStatus.Store(statusProbeFunc(func(inst *Instance) error {
		inst.mu.Lock()
		defer inst.mu.Unlock()
		prev := inst.Status
		if !inst.statusSampledLive {
			prev = ""
		}
		inst.statusSampledLive = true
		apply, next, _ := debounceFlipFromRunning(prev, sample, string(sample), inst.hookStatus, inst.tmuxFlipFromRunningPending)
		inst.Status = apply
		inst.tmuxFlipFromRunningPending = next
		return nil
	}))

	d := NewTransitionDaemon()
	d.turnLiveCheck = func(*Instance) bool { return false }
	d.syncProfile(profile) // pass 1: running observed live
	if got := d.lastStatus[profile][inst.ID]; got != "running" {
		t.Fatalf("pass 1 = %q, want running", got)
	}
	sample = StatusWaiting
	d.syncProfile(profile) // pass 2: first waiting sample must be HELD
	if got := d.lastStatus[profile][inst.ID]; got != "running" {
		t.Fatalf("pass 2 = %q, want running (one-sample hold carried across the Instance reload)", got)
	}
	if prior := d.livePrior[profile][inst.ID]; !prior.flipPending {
		t.Fatal("pass 2 must carry the pending flip to the next pass")
	}
	d.syncProfile(profile) // pass 3: second consecutive waiting sample confirms
	if got := d.lastStatus[profile][inst.ID]; got != "waiting" {
		t.Fatalf("pass 3 = %q, want waiting (flip confirmed)", got)
	}
}
