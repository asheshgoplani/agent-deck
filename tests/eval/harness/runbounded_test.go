package harness

import (
	"errors"
	"os/exec"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"
)

// TestRunBounded_CapturesOutput verifies the happy path: a process that exits
// on its own has its combined stdout+stderr captured.
func TestRunBounded_CapturesOutput(t *testing.T) {
	cmd := exec.Command("sh", "-c", "echo out; echo err 1>&2")
	got := string(RunBounded(cmd, 5*time.Second))
	if !strings.Contains(got, "out") || !strings.Contains(got, "err") {
		t.Fatalf("expected combined stdout+stderr, got: %q", got)
	}
}

// TestRunBounded_KillsHangingProcess is the core regression guard for the
// agent-deck-eval-bin process leak: an invocation that never exits on its own
// (the TUI in a non-PTY harness) must be killed at the timeout instead of
// blocking until the Go test timeout and leaking the process.
func TestRunBounded_KillsHangingProcess(t *testing.T) {
	cmd := exec.Command("sleep", "30")
	start := time.Now()
	RunBounded(cmd, 200*time.Millisecond)
	elapsed := time.Since(start)

	if elapsed > 3*time.Second {
		t.Fatalf("RunBounded did not return promptly after timeout: took %s", elapsed)
	}
	// A SIGKILL'd process is reaped but did not exit normally, so ProcessState
	// is non-nil with Exited()==false. The point is that it is no longer
	// running — ProcessState being set proves cmd.Wait() reaped it.
	if cmd.ProcessState == nil {
		t.Fatal("hanging process was not reaped: ProcessState is nil")
	}
	if cmd.ProcessState.Exited() {
		t.Fatalf("expected process to be signal-killed, not a normal exit: %v", cmd.ProcessState)
	}
}

// TestRunBounded_KillsProcessGroup proves children spawned by the bounded
// process are also killed — the TUI spawns helper processes (and, with a real
// tmux, sessions) that would otherwise outlive a bare Process.Kill().
func TestRunBounded_KillsProcessGroup(t *testing.T) {
	// Print the backgrounded child's PID, then hang so the timeout fires.
	cmd := exec.Command("sh", "-c", "sleep 30 & echo $!; sleep 30")
	out := string(RunBounded(cmd, 300*time.Millisecond))

	childPID, err := strconv.Atoi(strings.TrimSpace(out))
	if err != nil {
		t.Fatalf("could not parse child pid from %q: %v", out, err)
	}

	// The child must be gone (group SIGKILL). Poll briefly for reaping.
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if err := syscall.Kill(childPID, 0); errors.Is(err, syscall.ESRCH) {
			return // child reaped — group kill worked
		}
		time.Sleep(20 * time.Millisecond)
	}
	// Clean up the leak we just proved exists, then fail.
	_ = syscall.Kill(childPID, syscall.SIGKILL)
	t.Fatalf("child pid %d survived RunBounded — process group was not killed", childPID)
}
