//go:build capability_e2e

package capability

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// Issue #1214: kernel-exact task-worker completion. A worker dispatched to do a
// discrete task is run ONE-SHOT under `agent-deck run-task`. When the worker
// EXITS, the kernel delivers that edge exactly once (cmd.Wait); the wrapper
// parses the last #1186 sentinel (last-wins), writes a durable completion
// record, and wakes the child's parent exactly once via the notifier.
//
// This drives the REAL compiled binary end to end (run-task -> worker subprocess
// -> kernel exit -> durable record -> notifier emission). It asserts on the two
// durable artifacts a conductor relies on: the completion record and the single
// finished-event emission. Successful live-pane DELIVERY of the [DONE] line into
// a running conductor pane is the unit-tested notifier seam (internal/session),
// mirroring the boundary stated by TestCapability_Conductor_FinishedSignal.
//
// Surfaces: CLI (run-task) + Persistence (completion record + transition log) +
// Cross-platform (os/exec + cmd.Wait). The mechanism is tool-agnostic — the
// worker here is a plain shell command, proving run-task is not claude-specific.

// runTask invokes the real `agent-deck run-task` wrapper with the child id in
// the env exactly as a launched pane would have it, running the given one-shot
// worker command. Returns combined output (best effort) and any wrapper error.
func (c *capSandbox) runTask(t *testing.T, childID string, worker ...string) (string, error) {
	t.Helper()
	args := append([]string{"run-task", "--child", childID, "--"}, worker...)
	cmd := exec.Command(c.BinPath, args...)
	cmd.Env = append(c.Env(), "AGENTDECK_INSTANCE_ID="+childID, "AGENTDECK_PROFILE=default")
	cmd.Dir = c.Home
	out, err := cmd.CombinedOutput()
	return string(out), err
}

func (c *capSandbox) completionRecord(t *testing.T, childID string) (map[string]any, bool) {
	t.Helper()
	path := filepath.Join(c.Home, ".agent-deck", "runtime", "completions", childID+".json")
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, false
	}
	var rec map[string]any
	if err := json.Unmarshal(raw, &rec); err != nil {
		t.Fatalf("parse completion record: %v\nraw: %s", err, raw)
	}
	return rec, true
}

func (c *capSandbox) finishedLinesFor(childID string) int {
	n := 0
	for _, ln := range c.transitionLogLinesFor(childID) {
		if strings.Contains(ln, `"kind":"finished"`) {
			n++
		}
	}
	return n
}

func TestCapability_TaskWorker_ParentWokenExactlyOnceOnDone(t *testing.T) {
	c := newCapSandbox(t)

	// A non-conductor parent so parent resolution succeeds without a live-pane
	// gate; the wake EMISSION (one finished event) is what we assert. A claude
	// child gives a realistic dispatched-worker shape in the registry.
	c.run(t, "add", "-c", "bash", "-t", "tw-parent", c.WorkDir)
	c.run(t, "add", "-c", "claude", "-t", "tw-child", "--parent", "tw-parent", c.WorkDir)
	row, ok := c.findByTitle(t, "tw-child")
	if !ok {
		t.Fatalf("child not created: %+v", c.list(t))
	}
	childID := row.ID

	// One-shot worker: simulate a retry (fail sentinel) then the real outcome
	// (ok), then EXIT. A correct wrapper reports the FINAL outcome exactly once
	// (last-wins), never twice.
	worker := []string{"sh", "-c",
		"echo working; " +
			"echo '===AGENTDECK_DONE=== status=fail summary=transient retry'; " +
			"echo '===AGENTDECK_DONE=== status=ok summary=task complete'; " +
			"exit 0"}

	if out, err := c.runTask(t, childID, worker...); err != nil {
		t.Fatalf("run-task: %v\n%s", err, out)
	}

	// Durable completion record: exactly one, finalized, last-wins status ok.
	rec, ok := c.completionRecord(t, childID)
	if !ok {
		t.Fatalf("no durable completion record written for %s", childID)
	}
	if rec["status"] != "ok" {
		t.Fatalf("completion status = %v, want ok (last-wins over the fail retry)", rec["status"])
	}
	if !strings.Contains(toStr(rec["summary"]), "task complete") {
		t.Errorf("completion summary = %v, want the final summary", rec["summary"])
	}

	// Exactly one wake of the parent — no flood.
	if got := c.finishedLinesFor(childID); got != 1 {
		t.Fatalf("parent woken %d times, want exactly 1 (no flood/miss)", got)
	}

	snapshot(t, "taskworker-done-once",
		"durable completion record (kernel-exit captured the one-shot worker):\n"+
			mustPretty(t, rec)+
			"\n\nparent woken exactly once via the notifier (finished events for child): 1")
}
