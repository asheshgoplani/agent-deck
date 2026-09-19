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
//
// Review round 4 P2: the parent's hook handler renders the same rows into the
// UserPromptSubmit/SessionStart context (buildChildrenContextSummary) and
// must never capture a pane: zero tmux subprocesses per child, no sample
// taken, the light read from the hook file and the evidence the polling
// surfaces left behind.
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

	poll := func(sampling childRowsSampling) childRow {
		t.Helper()
		kids := []*session.Instance{kid}
		session.RefreshInstancesForCLIStatus(kids)
		before := tmux.SubprocessStarts()
		rows := buildChildRows(kids, sampling)
		calls := tmux.SubprocessStarts() - before
		t.Logf("buildChildRows(sampling=%v): %d tmux subprocesses for 1 child", sampling, calls)
		if sampling == sampleChildPanes && (calls < 1 || calls > 3) {
			t.Fatalf("children poll started %d tmux subprocesses for one child, want the single capture (1..3 incl. liveness checks)", calls)
		}
		if sampling == cachedChildStatus && calls != 0 {
			t.Fatalf("hook-context path started %d tmux subprocesses for one child, want 0 (hook handlers never capture)", calls)
		}
		if len(rows) != 1 {
			t.Fatalf("rows = %d, want 1", len(rows))
		}
		return rows[0]
	}

	// Hook-context path first: nothing captured, the fresh running hook holds.
	if r := poll(cachedChildStatus); r.Status != "running" {
		t.Fatalf("hook-context status = %q, want running", r.Status)
	}
	// Poll 1: the fresh running hook holds; the capture is the first sample.
	if r := poll(sampleChildPanes); r.Status != "running" {
		t.Fatalf("poll 1 status = %q, want running (one sample never flips)", r.Status)
	}
	time.Sleep(tmux.CompletedTurnSampleInterval + 200*time.Millisecond)
	// The hook-context path in between takes no sample: one idle sample on
	// record is still one, so the light stays running.
	if r := poll(cachedChildStatus); r.Status != "running" {
		t.Fatalf("hook-context status after one sample = %q, want running (no capture, so no second sample)", r.Status)
	}
	// Poll 2: the second independent sample confirms the lag in this pass.
	if r := poll(sampleChildPanes); r.Status != "waiting" {
		t.Fatalf("poll 2 status = %q, want waiting (hook lag confirmed by the children poll's own captures)", r.Status)
	}
	// The hook-context path reads the confirmed evidence without a capture.
	if r := poll(cachedChildStatus); r.Status != "waiting" {
		t.Fatalf("hook-context status after confirmation = %q, want waiting (from the cached evidence)", r.Status)
	}
}
