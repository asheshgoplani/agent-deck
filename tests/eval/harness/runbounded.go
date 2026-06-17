package harness

import (
	"bytes"
	"fmt"
	"os/exec"
	"syscall"
	"time"
)

// RunBounded starts cmd in its own process group, captures combined
// stdout+stderr, and guarantees the process AND its children are killed within
// timeout. It returns whatever output was captured before exit/kill.
//
// This exists for invocations that may launch the agent-deck TUI. The TUI
// never exits on its own in a non-PTY harness, so a plain cmd.CombinedOutput()
// blocks until the Go test timeout (default 10m, eval 6m) and then leaks the
// reparented agent-deck process — and any tmux sessions it spawned — past the
// test. Observed in the wild as orphaned `agent-deck -g work --select beta`
// processes lingering from agent-deck-eval-bin temp dirs. Setpgid + a
// group-wide SIGKILL on timeout closes that leak.
func RunBounded(cmd *exec.Cmd, timeout time.Duration) []byte {
	var buf bytes.Buffer
	// Stdout == Stderr (same writer) makes os/exec reuse a single child fd and
	// a single copy goroutine, so concurrent writes can't race the buffer —
	// the same guarantee cmd.CombinedOutput() relies on.
	cmd.Stdout = &buf
	cmd.Stderr = &buf
	// Own process group so we can signal the whole tree (the TUI plus any
	// children it forks) with a single kill(-pgid).
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}

	if err := cmd.Start(); err != nil {
		fmt.Fprintf(&buf, "\n[RunBounded] start error: %v", err)
		return buf.Bytes()
	}

	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()

	select {
	case <-done:
		// Exited on its own before the deadline.
	case <-time.After(timeout):
		// SIGKILL the whole process group (negative pid), then reap so the
		// copy goroutine finishes and buf is safe to read.
		_ = syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
		<-done
	}
	return buf.Bytes()
}
