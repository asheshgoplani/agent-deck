package session

import (
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/asheshgoplani/agent-deck/internal/tmux"
)

// Status-light audit defect B (2026-09-17): the hook file still said
// "running" eight minutes after the pane showed a completed turn
// ("✻ Sautéed for 3m 4s · done 9:08 PM") at an empty prompt. UpdateStatus took
// the fresh hook at face value, so the light stayed green and the CLI paired
// status=running with substate=idle-at-empty-prompt — a pair the substate's
// own contract forbids.
//
// Rule under test: when the hook says running but the pane shows a completed
// turn at an idle prompt with no busy cue for ≥ 2 consecutive passes, the
// light is waiting and the substate says hook-lag. Never on a single pass;
// never against a live spinner.

// auditConductorIdlePane is the captured conductor tail (account text
// replaced). The idle footer also carries the defect-F trap ("… +N lines" and
// "/clear to save Nk tokens" on separate lines).
const auditConductorIdlePane = "⏺ Bash(git -C ~/agent-deck log --oneline -3)\n" +
	"     … +24 lines (ctrl+o to expand)\n" +
	"⏺ Both children reported back; nothing else is pending.\n" +
	"✻ Sautéed for 3m 4s · done 9:08 PM\n" +
	"──────────────────────────────────────────────── conductor-agent-deck ─\n" +
	"❯ \n" +
	"────────────────────────────────────────────────────────────────────────\n" +
	"  [profile] user@host:~/.agent-deck/conductor/agent-deck | [Opus 5 (1M context)] ctx:38% in:380.8k out:1.2k\n" +
	"  ⏵⏵ bypass permissions on (shift+tab to cycle) · ← for agents\n" +
	"                                                new task? /clear to save 380.8k tokens\n"

const auditConductorBusyPane = "⏺ Bash(git -C ~/agent-deck log --oneline -3)\n" +
	"     … +24 lines (ctrl+o to expand)\n" +
	"✻ Sautéing… (2m 58s · ↓ 4.1k tokens · ctrl+c to interrupt)\n" +
	"                                                new task? /clear to save 380.8k tokens\n"

// writeHookLagStopFile writes the Stop event for the same hook session that
// writeHookFile bound (a different session_id would be rejected as a foreign
// ephemeral by UpdateHookStatus's ownership check).
func writeHookLagStopFile(t *testing.T, instanceID string) {
	t.Helper()
	body := fmt.Sprintf(`{"status":"waiting","session_id":"sess-610","event":"Stop","ts":%d}`,
		time.Now().Add(-time.Second).Unix())
	path := filepath.Join(GetHooksDir(), instanceID+".json")
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatalf("write hook: %v", err)
	}
}

// startHookLagInstance starts a real tmux pane rendering paneText and a fresh
// "running" hook file for it, then returns the instance ready for UpdateStatus.
func startHookLagInstance(t *testing.T, name, paneText string) (*Instance, func()) {
	t.Helper()
	skipIfNoTmuxBinary(t)
	tmpHome := t.TempDir()
	t.Setenv("HOME", tmpHome)

	panePath := filepath.Join(tmpHome, "pane.txt")
	if err := os.WriteFile(panePath, []byte(paneText), 0o644); err != nil {
		t.Fatal(err)
	}
	inst := NewInstanceWithTool("hook-lag-"+name, tmpHome, "claude")
	if err := inst.tmuxSession.Start(fmt.Sprintf("sh -c 'cat %q; exec sleep 3600'", panePath)); err != nil {
		t.Fatalf("tmux start: %v", err)
	}
	cleanup := func() { _ = inst.tmuxSession.Kill() }
	// The pane is a plain shell rendering a captured Claude frame; tell the
	// tmux layer whose renderings it is reading.
	inst.tmuxSession.Command = "claude"
	// Past UpdateStatus's 1.5s tmux grace window.
	time.Sleep(2 * time.Second)

	writeHookFile(t, inst.ID, "running", 10)
	RefreshInstancesForCLIStatus([]*Instance{inst})
	return inst, cleanup
}

func TestAudit_B_HookLagFlipsToWaitingAfterTwoPasses(t *testing.T) {
	inst, cleanup := startHookLagInstance(t, "idle", auditConductorIdlePane)
	defer cleanup()

	// Pass 1: the hook is fresh and says running. One pane sample is not
	// enough to overrule it — the light stays running — but the substate
	// already names the disagreement instead of claiming idle-at-empty-prompt.
	if err := inst.UpdateStatus(); err != nil {
		t.Fatalf("pass 1: %v", err)
	}
	if got := inst.GetStatusThreadSafe(); got != StatusRunning {
		t.Fatalf("pass 1 status = %q, want running (never flip on a single pass)", got)
	}
	if got := inst.Substate(); got != SubstateHookLag {
		t.Fatalf("pass 1 substate = %q, want %q", got, SubstateHookLag)
	}

	// Pass 2: a second independent sample of the same finished frame.
	time.Sleep(tmux.CompletedTurnSampleInterval + 200*time.Millisecond)
	if err := inst.UpdateStatus(); err != nil {
		t.Fatalf("pass 2: %v", err)
	}
	if got := inst.GetStatusThreadSafe(); got != StatusWaiting {
		t.Fatalf("pass 2 status = %q, want waiting (hook lag confirmed)", got)
	}
	if got := inst.Substate(); got != SubstateHookLag {
		t.Fatalf("pass 2 substate = %q, want %q", got, SubstateHookLag)
	}
	if got := inst.CachedSubstate(); got != SubstateHookLag {
		t.Fatalf("pass 2 cached substate = %q, want %q (TUI rows read the cache)", got, SubstateHookLag)
	}

	// The Stop hook finally lands: the lag is over and the ordinary hook
	// verdict applies again (waiting, idle-at-empty-prompt).
	writeHookLagStopFile(t, inst.ID)
	RefreshInstancesForCLIStatus([]*Instance{inst})
	if err := inst.UpdateStatus(); err != nil {
		t.Fatalf("pass 3: %v", err)
	}
	if got := inst.GetStatusThreadSafe(); got != StatusWaiting {
		t.Fatalf("pass 3 status = %q, want waiting", got)
	}
	if got := inst.Substate(); got != SubstateIdleAtEmptyPrompt {
		t.Fatalf("pass 3 substate = %q, want %q (lag cleared by the new hook event)", got, SubstateIdleAtEmptyPrompt)
	}
}

func TestAudit_B_LiveSpinnerNeverContradicted(t *testing.T) {
	inst, cleanup := startHookLagInstance(t, "busy", auditConductorBusyPane)
	defer cleanup()

	for pass := 1; pass <= 3; pass++ {
		if err := inst.UpdateStatus(); err != nil {
			t.Fatalf("pass %d: %v", pass, err)
		}
		if got := inst.GetStatusThreadSafe(); got != StatusRunning {
			t.Fatalf("pass %d status = %q, want running (live spinner on screen)", pass, got)
		}
		if got := inst.Substate(); got != SubstateRunning {
			t.Fatalf("pass %d substate = %q, want %q", pass, got, SubstateRunning)
		}
		time.Sleep(tmux.CompletedTurnSampleInterval + 200*time.Millisecond)
	}
}

// The contradictory pair is closed at the accessor too: whatever produced a
// running status, the substate never claims idle-at-empty-prompt beside it.
func TestReconcileSubstateWithStatus(t *testing.T) {
	cases := []struct {
		name   string
		status Status
		sub    Substate
		lagged bool
		want   Substate
	}{
		{"running + idle prompt, lag armed", StatusRunning, SubstateIdleAtEmptyPrompt, true, SubstateHookLag},
		{"running + idle prompt, no lag evidence", StatusRunning, SubstateIdleAtEmptyPrompt, false, SubstateNone},
		{"waiting + idle prompt", StatusWaiting, SubstateIdleAtEmptyPrompt, false, SubstateIdleAtEmptyPrompt},
		{"waiting after confirmed lag", StatusWaiting, SubstateIdleAtEmptyPrompt, true, SubstateHookLag},
		{"running + running", StatusRunning, SubstateRunning, false, SubstateRunning},
		{"running + running, stale lag evidence", StatusRunning, SubstateRunning, true, SubstateRunning},
		{"error + auth", StatusError, SubstateAuth401, false, SubstateAuth401},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := reconcileSubstateWithStatus(tc.status, tc.sub, tc.lagged); got != tc.want {
				t.Fatalf("got %q, want %q", got, tc.want)
			}
		})
	}
}
