package main

import (
	"os/exec"
	"testing"
)

// Regression coverage for the leaked-tmux-server class behind issue #1031's
// test harness. The launch-race tests run on an isolated `tmux -L <socket>`
// server and reap it via t.Cleanup. But t.Cleanup never runs when the test
// times out, panics hard, or the test binary is SIGKILL'd — and because the
// socket name used to be timestamp-derived, every such crashed run leaked a
// brand-new, uniquely-named `ad1031-*` server that no later run could ever
// reuse or reap. Observed in the wild as a pile of orphaned `tmux -L ad1031-*`
// servers consuming ptys.
//
// The fix makes the socket name deterministic per test and reaps any server
// left on it at SETUP (not just cleanup), so the next run of the same test
// inherits and kills the prior leak instead of stacking a new one.

// TestIsolatedTmuxSocket1031_IsDeterministic pins that the same test always
// resolves to the same socket name. Without this, a leaked server is
// unreachable by the next run and accumulates forever.
func TestIsolatedTmuxSocket1031_IsDeterministic(t *testing.T) {
	a := uniqueTmuxSocketName1031(t)
	b := uniqueTmuxSocketName1031(t)
	if a != b {
		t.Fatalf("socket name must be deterministic per test, got %q then %q", a, b)
	}
	// Keep it under the ~108-char Unix socket path limit (the original reason
	// the name was kept short).
	if len(a) > 24 {
		t.Fatalf("socket name too long for the socket-path limit: %q (%d chars)", a, len(a))
	}
}

// TestIsolatedTmuxSocket1031_ReapsPriorLeak proves the setup-time kill reaps a
// server left behind by a prior crashed run on the same deterministic socket.
func TestIsolatedTmuxSocket1031_ReapsPriorLeak(t *testing.T) {
	if _, err := exec.LookPath("tmux"); err != nil {
		t.Skip("tmux not on PATH")
	}

	socket := uniqueTmuxSocketName1031(t)
	// The socket name is deterministic, so a prior crashed run of THIS test may
	// have left a server (with the "leaked" session) on it — which would make
	// the new-session seed below fail with "duplicate session". Reap any such
	// leftover first so the seed starts from a clean server. Best-effort: a
	// missing server just makes kill-server a no-op.
	_ = exec.Command("tmux", "-L", socket, "kill-server").Run()
	// Simulate a server leaked by a previous timed-out/SIGKILL'd run.
	if err := exec.Command("tmux", "-L", socket, "new-session", "-d", "-s", "leaked").Run(); err != nil {
		t.Fatalf("seed leaked server: %v", err)
	}

	// isolatedTmuxSocket1031 must resolve to the SAME socket (deterministic)
	// and reap the pre-existing server at setup.
	got := isolatedTmuxSocket1031(t)
	if got != socket {
		t.Fatalf("helper resolved a different socket (%q) than the deterministic name (%q); a leak would be unreachable", got, socket)
	}

	if err := exec.Command("tmux", "-L", socket, "has-session", "-t", "leaked").Run(); err == nil {
		t.Fatal("prior leaked server should have been reaped at setup, but it is still alive")
	}
}
