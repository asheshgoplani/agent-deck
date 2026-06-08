//go:build capability_e2e

package capability

import (
	"strings"
	"testing"
	"time"
)

// waitForStatus polls the registry until the session reaches one of the wanted
// statuses, returning the last seen status and whether a match was found.
func (c *capSandbox) waitForStatus(t *testing.T, title string, timeout time.Duration, wanted ...string) (string, bool) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	var last string
	for time.Now().Before(deadline) {
		if row, ok := c.findByTitle(t, title); ok {
			last = row.Status
			for _, w := range wanted {
				if last == w {
					return last, true
				}
			}
		}
		time.Sleep(100 * time.Millisecond)
	}
	return last, false
}

// TestCapability_Lifecycle_Add proves `agent-deck add` writes a real registry
// row carrying the title, tool, group, and working directory the user passed.
func TestCapability_Lifecycle_Add(t *testing.T) {
	c := newCapSandbox(t)

	c.run(t, "add", "-c", "bash", "-t", "cap-add", "-g", "capgrp", c.WorkDir)

	row, ok := c.findByTitle(t, "cap-add")
	if !ok {
		t.Fatalf("after add, no registry row titled cap-add.\nrows: %+v", c.list(t))
	}
	// agent-deck canonicalizes the `bash` builtin to the tool name "shell".
	if row.Tool != "shell" {
		t.Errorf("tool = %q, want shell (the canonical name for the bash builtin)", row.Tool)
	}
	if !strings.Contains(row.Path, "project") {
		t.Errorf("path = %q, want it to contain the project workdir", row.Path)
	}
	if row.Group != "capgrp" {
		t.Errorf("group = %q, want capgrp", row.Group)
	}

	// Display proof: the registry as a human sees it, showing the row that the
	// assertions above checked field by field.
	snapshot(t, "add", c.run(t, "list"))
}

// TestCapability_Lifecycle_Start proves `session start` spawns a real tmux pane
// on the isolated socket and flips the registry status to an active state.
func TestCapability_Lifecycle_Start(t *testing.T) {
	c := newCapSandbox(t)
	c.run(t, "add", "-c", "bash", "-t", "cap-start", c.WorkDir)

	c.run(t, "session", "start", "cap-start")
	defer c.stopQuietly("cap-start")

	if name := c.waitForTmuxSession(t, 8*time.Second); name == "" {
		t.Fatalf("no agentdeck_ tmux session appeared after start.\nrows: %+v", c.list(t))
	}
	if got, ok := c.waitForStatus(t, "cap-start", 8*time.Second, "running", "starting", "idle", "waiting"); !ok {
		t.Fatalf("status did not reach an active state after start; last = %q", got)
	}

	// Display proof: the session-state view a human checks after start. The raw
	// pane of a freshly started bash session is only the shell login banner, so
	// we capture `status -v` instead, which shows the session listed under an
	// active state. That is the meaningful agent-deck content this capability
	// proves (the registry flipped to active), not shell chrome.
	snapshot(t, "start", c.run(t, "status", "-v"))
}

// TestCapability_Lifecycle_Stop proves `session stop` tears down the tmux pane
// and returns the registry status to stopped.
func TestCapability_Lifecycle_Stop(t *testing.T) {
	c := newCapSandbox(t)
	c.run(t, "add", "-c", "bash", "-t", "cap-stop", c.WorkDir)
	c.run(t, "session", "start", "cap-stop")
	if name := c.waitForTmuxSession(t, 8*time.Second); name == "" {
		t.Fatalf("session never started, cannot test stop")
	}

	c.run(t, "session", "stop", "cap-stop")

	if !c.waitForNoTmuxSession(t, 8*time.Second) {
		t.Fatalf("tmux session still present after stop: %v", c.tmuxSessionNames(t))
	}
	if got, ok := c.waitForStatus(t, "cap-stop", 5*time.Second, "stopped"); !ok {
		t.Fatalf("status did not return to stopped; last = %q", got)
	}

	// Display proof: pane is gone, so we show the registry returning the
	// session to stopped (the visible effect a human checks after stop).
	snapshot(t, "stop", c.run(t, "list"))
}

// TestCapability_Lifecycle_Restart proves `session restart` respawns the pane
// without leaving a duplicate (the #30 double-spawn guard) and ends running.
func TestCapability_Lifecycle_Restart(t *testing.T) {
	c := newCapSandbox(t)
	c.run(t, "add", "-c", "bash", "-t", "cap-restart", c.WorkDir)
	c.run(t, "session", "start", "cap-restart")
	defer c.stopQuietly("cap-restart")
	if name := c.waitForTmuxSession(t, 8*time.Second); name == "" {
		t.Fatalf("session never started, cannot test restart")
	}

	// --force skips the freshness guard so the restart proceeds even though we
	// only just started; asserting exactly one pane afterward is what proves
	// the guard against a double-spawn.
	c.run(t, "session", "restart", "cap-restart", "--force")

	if name := c.waitForTmuxSession(t, 8*time.Second); name == "" {
		t.Fatalf("no tmux session after restart")
	}
	if names := c.tmuxSessionNames(t); len(names) != 1 {
		t.Fatalf("expected exactly one pane after restart, got %d: %v", len(names), names)
	}
	if got, ok := c.waitForStatus(t, "cap-restart", 8*time.Second, "running", "starting", "idle", "waiting"); !ok {
		t.Fatalf("status not active after restart; last = %q", got)
	}

	// Display proof: the session-state view after restart. As with start, the
	// raw bash pane is only the shell banner, so we capture `status -v`, which
	// shows the single respawned session under an active state (the visible
	// effect that proves the restart succeeded without a duplicate).
	snapshot(t, "restart", c.run(t, "status", "-v"))
}

// TestCapability_Lifecycle_Rm proves `rm` drops a stopped session from the
// registry (happy path) and refuses a running session without --force
// (failure mode, the destructive-action guard).
func TestCapability_Lifecycle_Rm(t *testing.T) {
	c := newCapSandbox(t)

	// Failure mode first: rm on a running session must be refused.
	c.run(t, "add", "-c", "bash", "-t", "cap-rm-running", c.WorkDir)
	c.run(t, "session", "start", "cap-rm-running")
	defer c.stopQuietly("cap-rm-running")
	if name := c.waitForTmuxSession(t, 8*time.Second); name == "" {
		t.Fatalf("session never started, cannot test rm guard")
	}
	refusal, err := c.try("session", "remove", "cap-rm-running")
	if err == nil {
		t.Fatalf("session remove of a running session should be refused without --force, got success:\n%s", refusal)
	}
	if _, ok := c.findByTitle(t, "cap-rm-running"); !ok {
		t.Fatalf("refused rm must leave the session in the registry, but it is gone")
	}

	// Happy path: stop then rm removes the row.
	c.run(t, "add", "-c", "bash", "-t", "cap-rm-stopped", c.WorkDir)
	c.run(t, "session", "start", "cap-rm-stopped")
	if name := c.waitForTmuxSession(t, 8*time.Second); name == "" {
		t.Fatalf("cap-rm-stopped never started")
	}
	c.run(t, "session", "stop", "cap-rm-stopped")
	c.waitForNoTmuxSession(t, 8*time.Second)
	c.run(t, "session", "remove", "cap-rm-stopped")
	if _, ok := c.findByTitle(t, "cap-rm-stopped"); ok {
		t.Fatalf("after session remove, the row should be absent from the registry")
	}

	// Display proof: the destructive-action guard refusing a running session,
	// then the registry after the stopped session was removed.
	snapshot(t, "rm", "$ agent-deck session remove cap-rm-running\n"+
		strings.TrimRight(refusal, "\n")+
		"\n\n$ agent-deck list   (after stopping and removing cap-rm-stopped)\n"+
		c.run(t, "list"))
}

// TestCapability_Lifecycle_Fork proves the fork precondition guard: forking an
// unsupported session is refused with a clear error and creates no child row.
//
// The full happy paths (forks that inherit real Claude/Pi conversations, get a
// distinct id, and link ParentSessionID) need real tool session data, which is
// non-deterministic and key/tool-gated. Those paths are documented Tier N gaps.
// See docs/testing/capability-gaps.md.
func TestCapability_Lifecycle_Fork(t *testing.T) {
	c := newCapSandbox(t)
	c.run(t, "add", "-c", "bash", "-t", "cap-fork", c.WorkDir)

	out, err := c.try("session", "fork", "cap-fork")
	if err == nil {
		t.Fatalf("fork of a non-Claude session should be refused, got success:\n%s", out)
	}
	if !strings.Contains(out, "not a forkable session") {
		t.Errorf("fork refusal should name the precondition, got:\n%s", out)
	}
	if _, ok := c.findByTitle(t, "cap-fork-fork"); ok {
		t.Fatalf("a refused fork must not create a child registry row")
	}

	// Display proof: the precondition guard refusing to fork an unsupported
	// session.
	snapshot(t, "fork", "$ agent-deck session fork cap-fork\n"+strings.TrimRight(out, "\n"))
}
