//go:build capability_e2e

package capability

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// Issue #1225 — THE headline real-world proof. A child finishes WHILE the parent
// conductor is busy (mid-turn). The completion is committed to the parent's
// durable outbox (pull, not push). When the conductor's current turn ends, its
// Stop hook — the REAL compiled `agent-deck hook-handler` — drains the outbox
// and emits {decision:"block",reason:<completion>}, i.e. the busy parent STILL
// receives the completion at its very next turn boundary, exactly once.
//
// This is the exact scenario that failed live on 2026-05-29 (child dc3247c3 →
// parent 16c012a4 detected+targeted but never delivered; the push path can
// never land on an always-running conductor). No `claude -p`, no tmux send, no
// push — delivery is the consumer pulling at a provably-free moment.
//
// Surfaces: CLI (hook-handler) + Persistence (durable inbox) + Remote/Local
// (consumer-side change on the conductor, one change not per-child).

// runHookCapture invokes the real `agent-deck hook-handler` exactly as Claude
// Code does — AGENTDECK_INSTANCE_ID in env, JSON event on stdin — and returns
// its stdout (where the Stop-hook decision JSON is emitted). The handler always
// exits 0; stderr is folded in on failure only.
func (c *capSandbox) runHookCapture(t *testing.T, instanceID, payload string) string {
	t.Helper()
	cmd := exec.Command(c.BinPath, "hook-handler")
	cmd.Env = append(c.Env(), "AGENTDECK_INSTANCE_ID="+instanceID)
	cmd.Dir = c.Home
	cmd.Stdin = strings.NewReader(payload)
	out, err := cmd.Output()
	if err != nil {
		t.Fatalf("hook-handler: %v", err)
	}
	return string(out)
}

// commitToParentInbox writes one durable completion record to the parent's
// outbox, exactly as the producer does when a child finishes while the parent
// is busy. Writing the durable artifact directly IS the "committed while busy"
// state — the producer's only job is to land this record.
func (c *capSandbox) commitToParentInbox(t *testing.T, parentID string, rec map[string]any) {
	t.Helper()
	dir := filepath.Join(c.Home, ".agent-deck", "inboxes")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("mkdir inboxes: %v", err)
	}
	line, err := json.Marshal(rec)
	if err != nil {
		t.Fatalf("marshal inbox record: %v", err)
	}
	path := filepath.Join(dir, parentID+".jsonl")
	f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		t.Fatalf("open inbox: %v", err)
	}
	defer f.Close()
	if _, err := f.Write(append(line, '\n')); err != nil {
		t.Fatalf("write inbox: %v", err)
	}
}

func stopPayloadActive(stopHookActive bool) string {
	return fmt.Sprintf(`{"hook_event_name":"Stop","session_id":"sess-x","stop_hook_active":%t}`, stopHookActive)
}

func TestCapability_Issue1225_BusyParentReceivesCompletionAtTurnBoundary(t *testing.T) {
	c := newCapSandbox(t)

	// A conductor parent with a dispatched child, as the registry would hold them.
	c.run(t, "add", "-c", "bash", "-t", "conductor-busy", c.WorkDir)
	c.run(t, "add", "-c", "claude", "-t", "busy-child", "--parent", "conductor-busy", c.WorkDir)
	parent, ok := c.findByTitle(t, "conductor-busy")
	if !ok {
		t.Fatalf("conductor parent not created: %+v", c.list(t))
	}
	child, ok := c.findByTitle(t, "busy-child")
	if !ok {
		t.Fatalf("child not created: %+v", c.list(t))
	}

	// PHASE 1: the child finishes WHILE the parent is busy → durable commit only.
	c.commitToParentInbox(t, parent.ID, map[string]any{
		"child_session_id":  child.ID,
		"child_title":       "busy-child",
		"profile":           "default",
		"from_status":       "running",
		"to_status":         "waiting",
		"timestamp":         time.Now().Format(time.RFC3339Nano),
		"target_session_id": parent.ID,
		"turn_fingerprint":  child.ID + "@turn-busy-1",
	})

	// DURABILITY: while the parent stays busy, the record persists on disk.
	inboxPath := filepath.Join(c.Home, ".agent-deck", "inboxes", parent.ID+".jsonl")
	if _, err := os.Stat(inboxPath); err != nil {
		t.Fatalf("durability: record must persist while parent busy: %v", err)
	}

	// PHASE 2: the parent's turn ends → its real Stop hook drains and injects.
	out := c.runHookCapture(t, parent.ID, stopPayloadActive(false))
	var dec struct {
		Decision string `json:"decision"`
		Reason   string `json:"reason"`
	}
	if err := json.Unmarshal([]byte(strings.TrimSpace(out)), &dec); err != nil {
		t.Fatalf("Stop hook must emit a decision JSON, got %q: %v", out, err)
	}
	if dec.Decision != "block" {
		t.Fatalf("busy-parent proof: expected decision=block, got %q (stdout=%q)", dec.Decision, out)
	}
	if !strings.Contains(dec.Reason, child.ID) || !strings.Contains(dec.Reason, "busy-child") {
		t.Fatalf("injected reason missing the completion: %q", dec.Reason)
	}

	// EXACTLY ONCE: the parent's next turn boundary delivers nothing.
	out2 := c.runHookCapture(t, parent.ID, stopPayloadActive(true))
	if strings.TrimSpace(out2) != "" {
		t.Fatalf("exactly-once: second turn boundary must emit no decision, got %q", out2)
	}

	snapshot(t, "issue1225-busy-parent-proof",
		"A child finished while the conductor was BUSY. The completion was committed to\n"+
			"the durable per-parent outbox: "+inboxPath+"\n\n"+
			"At the conductor's next turn boundary, its real Stop hook drained the outbox\n"+
			"and injected the completion as the next turn's input:\n\n"+out+"\n"+
			"The second turn boundary delivered nothing (exactly once). No claude -p, no\n"+
			"tmux push — the busy parent PULLED the completion when it was provably free.")
}
