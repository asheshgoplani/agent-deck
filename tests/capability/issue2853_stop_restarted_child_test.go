//go:build capability_e2e

package capability

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// TestCapability_Issue2853_StopRestartedChildIsNotAnError runs the issue #2853
// report through the binary: a stub `claude` on PATH, a parent, a child made
// with `launch -parent`, then four cycles of session start, UserPromptSubmit,
// session stop, SessionEnd and `inbox drain parent --json`. The notify daemon
// polls (`notify-daemon --once`) after each step.
//
// On the unfixed code the start of cycle 2 read the child as error: the
// SessionEnd ("dead") hook from cycle 1's stop was still fresh, and the daemon
// applied it to the new pane. The parent got running -> error.
//
// Surfaces: CLI (launch, session start/stop/show, hook-handler, notify-daemon,
// inbox drain) + Persistence (registry status, hook file, parent inbox).
func TestCapability_Issue2853_StopRestartedChildIsNotAnError(t *testing.T) {
	c := newCapSandbox(t)
	stub := "#!/bin/sh\nprintf 'Claude Code stub\\n> '\nexec sleep 3600\n"
	if err := os.WriteFile(filepath.Join(c.ShimDir, "claude"), []byte(stub), 0o755); err != nil {
		t.Fatalf("write claude stub: %v", err)
	}

	c.run(t, "add", "-t", "parent", "-c", "claude", c.WorkDir)
	c.run(t, "session", "start", "parent")
	t.Cleanup(func() { c.stopQuietly("parent") })
	c.run(t, "launch", c.WorkDir, "-t", "child", "-c", "claude", "-parent", "parent")
	t.Cleanup(func() { c.stopQuietly("child") })
	child, ok := c.findByTitle(t, "child")
	if !ok {
		t.Fatalf("child not created.\nrows: %+v", c.list(t))
	}

	hook := func(event string) {
		c.runHook(t, child.ID, `{"hook_event_name":"`+event+`","session_id":"stub-`+child.ID+`"}`)
	}
	poll := func(cycle int, when string) {
		t.Helper()
		c.run(t, "notify-daemon", "--once")
		if st := c.issue2853Status(t, "child"); st == "error" {
			t.Fatalf("cycle %d (%s): child reads error", cycle, when)
		}
	}

	c.run(t, "notify-daemon", "--once")
	for cycle := 1; cycle <= 4; cycle++ {
		if cycle > 1 { // launch already started it; the report's first start is a no-op
			c.run(t, "session", "start", "child")
		}
		poll(cycle, "after session start")
		hook("UserPromptSubmit")
		poll(cycle, "after UserPromptSubmit")
		c.run(t, "session", "stop", "child")
		hook("SessionEnd")
		poll(cycle, "after stop and SessionEnd")
		if st := c.issue2853Status(t, "child"); st != "stopped" {
			t.Fatalf("cycle %d: child reads %q after the stop, want stopped", cycle, st)
		}
		for _, rec := range c.issue2853Drain(t, "parent") {
			if strings.EqualFold(rec.ToStatus, "error") {
				t.Fatalf("cycle %d: parent got %s -> %s (%s) for a deliberate stop",
					cycle, rec.FromStatus, rec.ToStatus, rec.DeliveryResult)
			}
		}
		// The hook ts has whole seconds; the report waits 6 s here.
		time.Sleep(1100 * time.Millisecond)
	}
}

func (c *capSandbox) issue2853Status(t *testing.T, ref string) string {
	t.Helper()
	var row struct {
		Status string `json:"status"`
	}
	out := c.issue2853Stdout(t, "session", "show", ref, "--json")
	if err := json.Unmarshal([]byte(strings.TrimSpace(out)), &row); err != nil {
		t.Fatalf("session show %s --json: %v\n%s", ref, err, out)
	}
	return row.Status
}

type issue2853Record struct {
	FromStatus     string `json:"from_status"`
	ToStatus       string `json:"to_status"`
	DeliveryResult string `json:"delivery_result"`
}

func (c *capSandbox) issue2853Drain(t *testing.T, ref string) []issue2853Record {
	t.Helper()
	out := strings.TrimSpace(c.issue2853Stdout(t, "inbox", "drain", ref, "--json"))
	if out == "" || out == "null" {
		return nil
	}
	var recs []issue2853Record
	if err := json.Unmarshal([]byte(out), &recs); err != nil {
		t.Fatalf("inbox drain %s --json: %v\n%s", ref, err, out)
	}
	return recs
}

// issue2853Stdout runs the binary and returns stdout alone: warnings go to
// stderr and must not break the JSON.
func (c *capSandbox) issue2853Stdout(t *testing.T, args ...string) string {
	t.Helper()
	cmd := exec.Command(c.BinPath, args...)
	cmd.Env = c.Env()
	cmd.Dir = c.Home
	out, err := cmd.Output()
	if err != nil {
		t.Fatalf("agent-deck %v: %v\n%s", args, err, out)
	}
	return string(out)
}
