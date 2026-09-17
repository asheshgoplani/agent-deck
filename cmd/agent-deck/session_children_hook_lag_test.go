package main

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"github.com/asheshgoplani/agent-deck/internal/session"
	"github.com/asheshgoplani/agent-deck/internal/tmux"
)

// childrenHookLagIdlePane is the captured conductor tail from the status-light
// audit (defect B): a finished turn at an empty prompt while the hook file
// still says running.
const childrenHookLagIdlePane = "⏺ Both children reported back; nothing else is pending.\n" +
	"✻ Sautéed for 3m 4s · done 9:08 PM\n" +
	"──────────────────────────────────────────────── conductor-agent-deck ─\n" +
	"❯ \n" +
	"────────────────────────────────────────────────────────────────────────\n" +
	"  ⏵⏵ bypass permissions on (shift+tab to cycle) · ← for agents\n"

// Review round 3 P2-4: `session children --json` (and --follow, which shares
// buildChildRows) is the documented parent poll channel, so it must take the
// same single pane capture per child that `list --json` takes; otherwise a
// conductor that only ever polls its children never accumulates the samples
// the hook-lag rule needs and the child's light stays green until the hook
// event ages out. Two calls ≥ CompletedTurnSampleInterval apart flip the
// child's row to waiting. The per-child cost is bounded and logged.
func TestChildrenRows_TakeTheHookLagSample(t *testing.T) {
	if _, err := exec.LookPath("tmux"); err != nil {
		t.Skip("tmux not available")
	}
	home := t.TempDir()
	t.Setenv("HOME", home)
	panePath := filepath.Join(home, "pane.txt")
	if err := os.WriteFile(panePath, []byte(childrenHookLagIdlePane), 0o644); err != nil {
		t.Fatal(err)
	}
	kid := session.NewInstanceWithTool("child-hook-lag", home, "claude")
	ts := kid.GetTmuxSession()
	if err := ts.Start(fmt.Sprintf("sh -c 'cat %q; exec sleep 3600'", panePath)); err != nil {
		t.Fatalf("tmux start: %v", err)
	}
	t.Cleanup(func() { _ = ts.Kill() })
	kid.Command = "claude"
	ts.Command = "claude"
	time.Sleep(2 * time.Second) // past UpdateStatus's tmux grace window

	hooksDir := session.GetHooksDir()
	if err := os.MkdirAll(hooksDir, 0o755); err != nil {
		t.Fatal(err)
	}
	hook := fmt.Sprintf(`{"status":"running","session_id":"sess-kid","event":"UserPromptSubmit","ts":%d}`,
		time.Now().Add(-10*time.Second).Unix())
	if err := os.WriteFile(filepath.Join(hooksDir, kid.ID+".json"), []byte(hook), 0o644); err != nil {
		t.Fatal(err)
	}

	poll := func() childRow {
		t.Helper()
		kids := []*session.Instance{kid}
		session.RefreshInstancesForCLIStatus(kids)
		before := tmux.SubprocessStarts()
		rows := buildChildRows(kids)
		calls := tmux.SubprocessStarts() - before
		t.Logf("buildChildRows: %d tmux subprocesses for 1 child", calls)
		if calls < 1 || calls > 3 {
			t.Fatalf("buildChildRows started %d tmux subprocesses for one child, want the single capture (1..3 incl. liveness checks)", calls)
		}
		if len(rows) != 1 {
			t.Fatalf("rows = %d, want 1", len(rows))
		}
		return rows[0]
	}

	// Poll 1: the fresh running hook holds; the capture is the first sample.
	if r := poll(); r.Status != "running" {
		t.Fatalf("poll 1 status = %q, want running (one sample never flips)", r.Status)
	}
	time.Sleep(tmux.CompletedTurnSampleInterval + 200*time.Millisecond)
	// Poll 2: the second independent sample confirms the lag in this pass.
	if r := poll(); r.Status != "waiting" {
		t.Fatalf("poll 2 status = %q, want waiting (hook lag confirmed by the children poll's own captures)", r.Status)
	}
}
