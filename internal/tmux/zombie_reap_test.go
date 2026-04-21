package tmux

// Zombie-reap regression coverage for issue #677.
//
// Before v1.7.43 the tmux ControlPipe only called cmd.Wait() inside Close(),
// so a `tmux -C attach-session` subprocess that died on its own (session
// killed externally, tmux server reload, etc.) left behind a zombie process
// table entry until Close() was eventually invoked. In practice, when the
// PipeManager reconnect loop gave up or a session was removed, Close() was
// skipped and zombies accumulated (observed: 10 zombies on a single TUI
// conductor during 2026-04-21 cascades).
//
// This file proves reader() now reaps on EOF. procStateIsZombie is also
// reused by mcppool and watcher zombie tests — kept local here to avoid
// cross-package test helpers.

import (
	"bytes"
	"fmt"
	"os"
	"os/exec"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// procStateIsZombie reports whether /proc/<pid>/status shows State "Z".
// Returns (false, nil) when the pid was reaped (ENOENT on status file).
func procStateIsZombie(pid int) (bool, error) {
	data, err := os.ReadFile(fmt.Sprintf("/proc/%d/status", pid))
	if err != nil {
		if os.IsNotExist(err) {
			return false, nil // reaped
		}
		return false, err
	}
	for _, line := range bytes.Split(data, []byte{'\n'}) {
		if bytes.HasPrefix(line, []byte("State:")) {
			// Format: "State:\tZ (zombie)"
			return bytes.Contains(line, []byte("Z")) && bytes.Contains(line, []byte("zombie")), nil
		}
	}
	return false, nil
}

// countZombieChildren returns the number of zombie processes whose parent
// PID matches parentPID. Linux-only (reads /proc). Returns 0 on non-Linux.
func countZombieChildren(t *testing.T, parentPID int) int {
	t.Helper()
	entries, err := os.ReadDir("/proc")
	if err != nil {
		t.Skipf("cannot read /proc (non-Linux?): %v", err)
		return 0
	}
	count := 0
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		var pid int
		if _, err := fmt.Sscanf(e.Name(), "%d", &pid); err != nil {
			continue
		}
		data, err := os.ReadFile(fmt.Sprintf("/proc/%d/status", pid))
		if err != nil {
			continue
		}
		var (
			ppid   int
			zombie bool
		)
		for _, line := range bytes.Split(data, []byte{'\n'}) {
			if bytes.HasPrefix(line, []byte("PPid:")) {
				_, _ = fmt.Sscanf(string(line), "PPid:\t%d", &ppid)
			} else if bytes.HasPrefix(line, []byte("State:")) && bytes.Contains(line, []byte("zombie")) {
				zombie = true
			}
		}
		if zombie && ppid == parentPID {
			count++
		}
	}
	return count
}

// makeZombieTestSession creates a tmux session on the isolated test socket
// without requiring a pre-existing "real" session (unlike createTestSession,
// which skips when only the bootstrap session is present — that guard is
// for legacy tests, not for #677 regression coverage).
func makeZombieTestSession(t *testing.T, suffix string) string {
	t.Helper()
	skipIfNoTmuxBinary(t)
	name := SessionPrefix + "zombiereap-" + suffix
	require.NoError(t, exec.Command("tmux", "new-session", "-d", "-s", name).Run(),
		"failed to create test session %s", name)
	t.Cleanup(func() { _ = exec.Command("tmux", "kill-session", "-t", name).Run() })
	return name
}

// TestControlPipe_NoZombie_WhenProcessExits proves the reader goroutine now
// reaps the tmux -C child when it dies on its own (external kill-session).
// Before the fix this left a zombie until Close() was called externally.
func TestControlPipe_NoZombie_WhenProcessExits(t *testing.T) {
	name := makeZombieTestSession(t, "nozombie-exit")

	pipe, err := NewControlPipe(name, "")
	require.NoError(t, err)
	// Intentionally do NOT defer pipe.Close() — the point of this test is
	// that reader() reaps on its own when the subprocess dies externally.

	pid := pipe.cmd.Process.Pid

	// Kill the tmux session externally. This causes `tmux -C attach-session`
	// to exit, which closes its stdout, which trips the reader's scanner EOF.
	require.NoError(t, exec.Command("tmux", "kill-session", "-t", name).Run())

	// Wait for the reader goroutine to notice and exit.
	select {
	case <-pipe.Done():
	case <-time.After(3 * time.Second):
		t.Fatal("pipe did not detect process exit within 3s")
	}

	// Give the reaper a moment — reap() runs in the reader's defer so it
	// should be complete by the time Done() is closed, but allow for
	// scheduling jitter.
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		zombie, err := procStateIsZombie(pid)
		require.NoError(t, err)
		if !zombie {
			return // reaped
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatalf("pid %d is still a zombie after reader exit — cmd.Wait() not called", pid)
}

// TestControlPipe_NoZombie_ManyCycles spawns and tears down many pipes to
// detect any steady-state zombie accumulation (the failure mode reported
// in #677 where 43+10 zombies piled up over hours).
func TestControlPipe_NoZombie_ManyCycles(t *testing.T) {
	const cycles = 20

	baseline := countZombieChildren(t, os.Getpid())

	for i := 0; i < cycles; i++ {
		name := makeZombieTestSession(t, fmt.Sprintf("cycle-%d", i))
		pipe, err := NewControlPipe(name, "")
		require.NoError(t, err)

		// Kill session externally (simulates tmux-server restart / manual kill).
		require.NoError(t, exec.Command("tmux", "kill-session", "-t", name).Run())

		select {
		case <-pipe.Done():
		case <-time.After(3 * time.Second):
			t.Fatalf("cycle %d: pipe did not detect process exit", i)
		}
	}

	// Allow any stragglers to reap.
	time.Sleep(200 * time.Millisecond)

	got := countZombieChildren(t, os.Getpid())
	assert.LessOrEqual(t, got, baseline, "zombie child count grew after %d cycles: baseline=%d got=%d", cycles, baseline, got)
}
