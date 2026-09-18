package main

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"testing"

	"github.com/asheshgoplani/agent-deck/internal/session"
	"github.com/asheshgoplani/agent-deck/internal/tmux"
)

// armWindowCloseSession creates a real isolated tmux session with two windows
// and wraps it in a session.Instance, so closeSessionWindow (the CLI parity
// path for the TUI's window-row 'd') can be exercised end to end without a
// full agent-deck registry/profile setup.
func armWindowCloseSession(t *testing.T) (*session.Instance, string, string) {
	t.Helper()
	if _, err := exec.LookPath("tmux"); err != nil {
		t.Skip("tmux binary not on PATH; skipping")
	}

	socket := fmt.Sprintf("wcc%d", os.Getpid())
	target := "agentdeck_windowclose_cli"
	if out, err := exec.Command("tmux", "-L", socket, "new-session", "-d", "-x", "80", "-y", "24", "-s", target, "sleep", "300").CombinedOutput(); err != nil {
		t.Fatalf("create tmux session: %v: %s", err, out)
	}
	t.Cleanup(func() {
		_ = exec.Command("tmux", "-L", socket, "kill-server").Run()
	})
	if out, err := exec.Command("tmux", "-L", socket, "new-window", "-t", target, "sleep", "300").CombinedOutput(); err != nil {
		t.Fatalf("new-window: %v: %s", err, out)
	}

	inst := &session.Instance{ID: "cli-wc-1", Title: "cli-wc", Status: session.StatusRunning}
	inst.SetTmuxSessionForTest(&tmux.Session{Name: target, SocketName: socket})
	return inst, socket, target
}

func windowCountViaCLI(t *testing.T, socket, target string) int {
	t.Helper()
	out, err := exec.Command("tmux", "-L", socket, "list-windows", "-t", target, "-F", "#{window_index}").CombinedOutput()
	if err != nil {
		t.Fatalf("list-windows: %v: %s", err, out)
	}
	return len(strings.Fields(strings.TrimSpace(string(out))))
}

// TestCloseSessionWindow_KillsExtraWindow proves `agent-deck session window
// close` (via its underlying closeSessionWindow) can kill a non-last window,
// leaving the others intact — CLI parity for the TUI's kill-window confirm.
func TestCloseSessionWindow_KillsExtraWindow(t *testing.T) {
	inst, socket, target := armWindowCloseSession(t)
	if got := windowCountViaCLI(t, socket, target); got != 2 {
		t.Fatalf("setup: window count = %d, want 2", got)
	}

	if err := closeSessionWindow(inst, 1); err != nil {
		t.Fatalf("closeSessionWindow: %v", err)
	}
	if got := windowCountViaCLI(t, socket, target); got != 1 {
		t.Fatalf("window count after close = %d, want 1", got)
	}
}

// TestCloseSessionWindow_RefusesLastWindow proves the CLI path refuses to
// kill a session's only window, same as the TUI guard.
func TestCloseSessionWindow_RefusesLastWindow(t *testing.T) {
	inst, socket, target := armWindowCloseSession(t)
	// Kill the extra window first so only one remains.
	if out, err := exec.Command("tmux", "-L", socket, "kill-window", "-t", target+":1").CombinedOutput(); err != nil {
		t.Fatalf("kill-window (setup): %v: %s", err, out)
	}
	if got := windowCountViaCLI(t, socket, target); got != 1 {
		t.Fatalf("setup: window count = %d, want 1", got)
	}

	err := closeSessionWindow(inst, 0)
	if !errors.Is(err, tmux.ErrLastWindow) {
		t.Fatalf("closeSessionWindow on the last window: err = %v, want ErrLastWindow", err)
	}
	if got := windowCountViaCLI(t, socket, target); got != 1 {
		t.Fatalf("window count after refused close = %d, want 1", got)
	}
}

// TestCloseSessionWindow_UnknownIndexIsNotFound proves an out-of-range index
// reports session-window-not-found rather than a bare tmux error.
func TestCloseSessionWindow_UnknownIndexIsNotFound(t *testing.T) {
	inst, _, _ := armWindowCloseSession(t)
	err := closeSessionWindow(inst, 99)
	if !errors.Is(err, errSessionWindowNotFound) {
		t.Fatalf("closeSessionWindow on unknown index: err = %v, want errSessionWindowNotFound", err)
	}
}
