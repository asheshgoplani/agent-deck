package tmux

import (
	"crypto/sha256"
	"fmt"
	"os/exec"
	"strings"
	"testing"
	"time"
)

// Regression guard for #1567 (Hermes Agent dies <100ms) and #1580 (`npx codex`
// dies instantly).
//
// Root cause: tmux delivers a SINGLE trailing string command to new-session /
// respawn-pane through the server's default-shell (`$SHELL -c "…"`). That
// implicit wrap kills fast-TTY-acquiring tools within milliseconds, because
// the tool is no longer the pane leader tmux execvp()'d directly. When the
// command is instead passed as SEPARATE argv tokens, tmux execvp()s the first
// token as the pane's initial process and the default-shell is never involved
// — the exact form the #1567 reporter proved survives.
//
// The production-environment death depends on the tool's controlling-terminal
// timing, which is not deterministic in CI. These tests substitute a HOSTILE
// default-shell (`false(1)`) on an isolated tmux server: any spawn routed
// through the default-shell dies instantly and deterministically, while a
// direct-argv spawn is untouched by it. That cleanly separates the two tmux
// code paths:
//
//   - single-string spawn  → default-shell wrap → dies under the hostile shell
//   - argv-token spawn     → direct execvp      → survives the hostile shell
//
// TestIssue1567_StartCommandSpecSpawnSurvivesHostileShell exercises the exact
// production arg vector and FAILS on the pre-fix code (which appended one
// shell-quoted `bash -c '…'` string).

// hostileShellServer starts a detached tmux server on a private -L socket and
// sets its global default-shell to false(1), so any command tmux routes
// through the default-shell dies instantly. Returns the socket name.
func hostileShellServer(t *testing.T) string {
	t.Helper()
	if _, err := exec.LookPath("tmux"); err != nil {
		t.Skip("tmux not available")
	}
	falsePath, err := exec.LookPath("false")
	if err != nil {
		t.Skip("false(1) not available")
	}

	socket := fmt.Sprintf("ad%x", sha256.Sum256([]byte(t.Name())))[:14]
	kill := func() {
		_ = exec.Command("tmux", "-L", socket, "kill-server").Run()
	}
	// Deterministic socket per test — clear any server leaked by a prior
	// aborted run before creating a fresh one.
	kill()

	// Keeper session holds the server alive while test sessions come and go.
	// Spawned via argv tokens so it is immune to the hostile shell we set next.
	if out, err := exec.Command("tmux", "-L", socket, "new-session",
		"-d", "-x", "80", "-y", "24", "-s", "keeper", "bash").CombinedOutput(); err != nil {
		t.Fatalf("create keeper session: %v: %s", err, out)
	}
	t.Cleanup(kill)

	if out, err := exec.Command("tmux", "-L", socket, "set-option",
		"-g", "default-shell", falsePath).CombinedOutput(); err != nil {
		t.Fatalf("set hostile default-shell: %v: %s", err, out)
	}
	return socket
}

// sessionAlive reports whether the named session still exists on the socket
// after a short grace period (long enough for a default-shell wrapped command
// to die — production deaths are <100ms; we allow 10x that).
func sessionAlive(t *testing.T, socket, name string) bool {
	t.Helper()
	time.Sleep(1 * time.Second)
	err := exec.Command("tmux", "-L", socket, "has-session", "-t", "="+name).Run()
	return err == nil
}

// TestIssue1567_SingleStringSpawnDiesUnderHostileShell pins the FAILING code
// path: a single-string command is delivered through the default-shell, so
// under the hostile shell the pane (and session) dies instantly. This is the
// deterministic stand-in for the <100ms Hermes death in #1567.
func TestIssue1567_SingleStringSpawnDiesUnderHostileShell(t *testing.T) {
	socket := hostileShellServer(t)

	// Session may be created and then die within milliseconds; new-session
	// itself can succeed either way, so only the aliveness check matters.
	_ = exec.Command("tmux", "-L", socket, "new-session",
		"-d", "-s", "single", "sleep 30").Run()

	if sessionAlive(t, socket, "single") {
		t.Fatal("single-string spawn survived under hostile default-shell; " +
			"expected the default-shell wrap to kill it — the #1567 failure " +
			"mode stand-in no longer reproduces")
	}
}

// TestIssue1567_ArgvSpawnSurvivesHostileShell proves the FIXED code path: argv
// tokens make tmux execvp() the command directly, bypassing the default-shell
// entirely, so the hostile shell cannot kill it.
func TestIssue1567_ArgvSpawnSurvivesHostileShell(t *testing.T) {
	socket := hostileShellServer(t)

	if out, err := exec.Command("tmux", "-L", socket, "new-session",
		"-d", "-s", "argv", "bash", "-c", "sleep 30").CombinedOutput(); err != nil {
		t.Fatalf("argv new-session: %v: %s", err, out)
	}

	if !sessionAlive(t, socket, "argv") {
		t.Fatal("argv-token spawn died under hostile default-shell; " +
			"direct execvp must not involve the default-shell")
	}
}

// TestIssue1567_StartCommandSpecSpawnSurvivesHostileShell executes the EXACT
// arg vector startCommandSpec produces against the hostile server. On the
// pre-fix code (single shell-quoted `bash -c '…'` string) the session dies;
// with the argv-token fix it survives. This is the production regression
// guard for #1567/#1580.
func TestIssue1567_StartCommandSpecSpawnSurvivesHostileShell(t *testing.T) {
	socket := hostileShellServer(t)

	s := &Session{
		Name:                       "prodspec",
		SocketName:                 socket,
		RunCommandAsInitialProcess: true,
	}
	launcher, args := s.startCommandSpec("/tmp", "sleep 30")
	if launcher != "tmux" {
		t.Fatalf("expected tmux launcher in direct mode, got %q (args: %v)", launcher, args)
	}

	if out, err := exec.Command(launcher, args...).CombinedOutput(); err != nil {
		t.Fatalf("startCommandSpec spawn failed: %v: %s\nargs: %v", err, out, args)
	}

	if !sessionAlive(t, socket, "prodspec") {
		t.Fatalf("session spawned via startCommandSpec died under hostile "+
			"default-shell — the command is being routed through the server "+
			"default-shell instead of direct argv execvp (#1567/#1580)\nargs: %s",
			strings.Join(args, " | "))
	}
}
