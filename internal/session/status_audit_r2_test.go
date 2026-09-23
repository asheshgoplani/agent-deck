package session

import (
	"fmt"
	"testing"
	"time"
)

// Status-detection audit, review round 2 (2026-09-23).

// claudeAwaitedAgentsAboveRoster is a Claude pane whose turn ended by handing
// off to background agents ("✻ Waiting for 3 background agents to finish").
// Claude draws one roster row per agent under the footer, so the sessions that
// show this line are the ones with a long roster, and the rows push the line
// out of the 20-line background-work window. Without the roster trim the
// session read as waiting while Claude was still owed its agents' results.
func claudeAwaitedAgentsAboveRoster(rows int) string {
	frame := "⏺ Launched the reviewers. Ending my turn until they report.\n" +
		"\n" +
		"✻ Waiting for 3 background agents to finish\n" +
		"\n" +
		"──────────────────────────────────────────────────────────── work ─\n" +
		"❯ \n" +
		"────────────────────────────────────────────────────────────────────\n" +
		"  [p] u@host:/x | [Fable 5.1] ctx:41% in:408.7k out:1.1k\n" +
		"  ⏵⏵ bypass permissions on (shift+tab to cycle) · ← for agents\n" +
		"  ⏺ main\n"
	for i := 0; i < rows; i++ {
		frame += fmt.Sprintf("  ◯ general-purpose  Task %d: review slice %d\n", i+1, i+1)
	}
	return frame
}

// P1-2, tmux path (GetStatus): a fresh process with no hook.
func TestAuditR2_ClaudeAwaitedAgentsAboveRosterStaysRunning(t *testing.T) {
	inst, cleanup := startPaneInstance(t, "claude", "claude-awaited-roster", claudeAwaitedAgentsAboveRoster(16))
	defer cleanup()
	storage, err := NewStorageWithProfile("_test-audit-r2-awaited-roster")
	if err != nil {
		t.Fatalf("storage: %v", err)
	}
	defer storage.Close()
	fresh := persistAndReload(t, storage, inst, StatusRunning)
	if status, _ := cliPass(t, fresh); status != StatusRunning {
		t.Fatalf("fresh process = %q, want running: the turn awaits 3 background agents above a 16-row roster", status)
	}
}

// P1-2, hook path (updateStatus → BackgroundWorkPending): Claude's Stop hook
// fires when the foreground turn hands off; the pane decides whether the
// session is still owed a background agent.
func TestAuditR2_ClaudeStopHookAwaitedAgentsAboveRosterStaysRunning(t *testing.T) {
	inst, cleanup := startHookLagInstance(t, "awaited-roster", claudeAwaitedAgentsAboveRoster(16))
	defer cleanup()
	writeHookLagStopFile(t, inst.ID)
	if status, _ := cliPass(t, inst); status != StatusRunning {
		t.Fatalf("Stop hook + awaited agents above roster = %q, want running", status)
	}
}

// P3-6: a pass that takes the TUI-alive path must drop the carried priors so
// an hours-old verdict is not re-seeded when the TUI exits.
func TestAuditR2_DaemonDropsLivePriorWhileTUIAlive(t *testing.T) {
	inboxTestHome(t)
	profile := "_test-audit-r2-liveprior-tui"
	storage, err := NewStorageWithProfile(profile)
	if err != nil {
		t.Fatalf("storage: %v", err)
	}
	defer storage.Close()
	inst := &Instance{
		ID: "audit-r2-prior", Title: "worker", ProjectPath: t.TempDir(), GroupPath: DefaultGroupPath,
		Tool: "codex", Status: StatusRunning, CreatedAt: time.Now(),
	}
	if err := storage.SaveWithGroups([]*Instance{inst}, nil); err != nil {
		t.Fatalf("save: %v", err)
	}
	db := storage.GetDB()
	if db == nil {
		t.Fatal("no state db")
	}
	if err := db.RegisterInstance(false); err != nil {
		t.Fatalf("RegisterInstance: %v", err)
	}
	if err := db.Heartbeat(); err != nil {
		t.Fatalf("Heartbeat: %v", err)
	}

	d := NewTransitionDaemon()
	d.storages[profile] = storage
	d.livePrior[profile] = map[string]liveStatusPrior{inst.ID: {status: StatusRunning, flipPending: true}}
	d.syncProfile(profile)
	if priors, ok := d.livePrior[profile]; ok && len(priors) > 0 {
		t.Fatalf("TUI-alive pass kept stale priors: %+v", priors)
	}
}
