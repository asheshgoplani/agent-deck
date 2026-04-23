package tmux

import (
	"errors"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"syscall"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// runSoftkillHelper implements two child-process behaviours selected by env.
// Dispatched from testmain_test.go's TestMain when SOFTKILL_TEST_HELPER is
// set. We cannot rely on /bin/sh traps because dash/bash on Linux delay trap
// dispatch until the foreground `sleep` returns — SIGTERM-while-sleeping is
// equivalent to SIGKILL from the parent's perspective. A Go helper using
// signal.Notify handles SIGTERM instantly.
//   - "clean": on SIGTERM, touch $MARKER then exit 0 (must run in < grace).
//   - "ignore": install a no-op handler for SIGTERM so it is ignored, then
//     block forever. Parent must fall back to SIGKILL.
func runSoftkillHelper(role string) {
	ch := make(chan os.Signal, 1)
	signal.Notify(ch, syscall.SIGTERM)
	switch role {
	case "clean":
		<-ch
		if marker := os.Getenv("MARKER"); marker != "" {
			_ = os.WriteFile(marker, []byte("ok"), 0o644)
		}
		os.Exit(0)
	case "ignore":
		// Drain TERM signals indefinitely — parent must SIGKILL.
		go func() {
			for range ch {
			}
		}()
		select {} // block forever
	default:
		os.Exit(2)
	}
}

// spawnHelper starts the test binary in helper mode and returns the cmd.
// Caller is responsible for reaping.
func spawnHelper(t *testing.T, role string, extraEnv ...string) *exec.Cmd {
	t.Helper()
	exe, err := os.Executable()
	require.NoError(t, err)
	cmd := exec.Command(exe, "-test.run=^$") // run no tests in child
	env := append(os.Environ(), "SOFTKILL_TEST_HELPER="+role)
	env = append(env, extraEnv...)
	cmd.Env = env
	// Isolate child so it doesn't write to the parent's test output.
	cmd.Stdout = nil
	cmd.Stderr = nil
	require.NoError(t, cmd.Start())
	return cmd
}

// waitForPidAlive polls until syscall.Kill(pid, 0) returns nil (process
// exists) or the deadline passes. Used to ensure the helper has installed
// its signal handler before the parent sends SIGTERM.
func waitForPidAlive(pid int, d time.Duration) {
	deadline := time.Now().Add(d)
	for time.Now().Before(deadline) {
		if err := syscall.Kill(pid, 0); err == nil {
			// alive — give it a beat to install signal.Notify
			time.Sleep(50 * time.Millisecond)
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// TestKillStaleControlClients_TerminatesCleanlyOnSIGTERM asserts that
// softKillProcess sends SIGTERM first and allows the target to shut down
// cleanly — proven by a marker file the child writes from its SIGTERM
// handler (SIGKILL cannot be trapped, so the marker's existence is
// conclusive evidence the TERM path ran).
//
// Regression guard for #737: the prior implementation sent SIGKILL
// unconditionally, which on macOS Homebrew tmux 3.6a races an unfixed
// NULL-deref in the control-mode notify path and destroys the server.
func TestKillStaleControlClients_TerminatesCleanlyOnSIGTERM(t *testing.T) {
	tmpDir := t.TempDir()
	marker := filepath.Join(tmpDir, "term-handled")

	cmd := spawnHelper(t, "clean", "MARKER="+marker)
	pid := cmd.Process.Pid

	// Reap concurrently so softKillProcess's syscall.Kill(pid, 0) poll
	// sees ESRCH as soon as the child actually exits, instead of seeing
	// a zombie and falsely escalating. Production has the init system
	// reaping stale control clients; the test has to emulate that.
	waitDone := make(chan struct{})
	go func() {
		_, _ = cmd.Process.Wait()
		close(waitDone)
	}()
	t.Cleanup(func() {
		_ = cmd.Process.Kill()
		<-waitDone
	})

	// Give the helper time to install its signal handler.
	waitForPidAlive(pid, 1*time.Second)

	_ = softKillProcess(pid, 500*time.Millisecond)

	// Let the reaper goroutine finish before asserting on the marker.
	select {
	case <-waitDone:
	case <-time.After(500 * time.Millisecond):
	}

	// Marker must exist — proves the child actually ran its SIGTERM
	// handler. SIGKILL cannot be trapped, so a missing marker means
	// softKillProcess skipped SIGTERM and went straight to SIGKILL —
	// exactly the #737 regression we're guarding against.
	//
	// We deliberately do NOT assert on softKillProcess's return value
	// (usedSIGKILL). When the child is a subprocess of the test binary
	// the zombie cannot be reclaimed until cmd.Wait() returns, and the
	// Go runtime may not schedule the reaper goroutine fast enough for
	// softKillProcess's kill(pid, 0) probe to see ESRCH inside the grace
	// window. In production, control clients are children of tmux (not
	// of agent-deck), so tmux reaps them promptly and ESRCH is observed
	// cleanly. Asserting on marker-existence captures the real
	// regression guarantee (SIGTERM ran before SIGKILL) without the
	// test-specific zombie race.
	_, err := os.Stat(marker)
	assert.NoError(t, err, "child's SIGTERM handler must have run (marker file must exist)")

	// Process should be gone.
	err = syscall.Kill(pid, 0)
	assert.True(t, errors.Is(err, syscall.ESRCH), "child process should be fully reaped; got err=%v", err)
}

// TestKillStaleControlClients_FallsBackToSIGKILL asserts that when the
// target ignores SIGTERM, softKillProcess still kills it via SIGKILL
// within roughly the grace window. This preserves the original
// killStaleControlClients guarantee that stale control clients cannot
// linger indefinitely.
func TestKillStaleControlClients_FallsBackToSIGKILL(t *testing.T) {
	// Helper installs a no-op SIGTERM handler and blocks forever.
	cmd := spawnHelper(t, "ignore")
	pid := cmd.Process.Pid

	waitDone := make(chan struct{})
	go func() {
		_, _ = cmd.Process.Wait()
		close(waitDone)
	}()
	t.Cleanup(func() {
		_ = cmd.Process.Kill()
		<-waitDone
	})

	waitForPidAlive(pid, 1*time.Second)

	start := time.Now()
	usedSIGKILL := softKillProcess(pid, 500*time.Millisecond)
	elapsed := time.Since(start)

	select {
	case <-waitDone:
	case <-time.After(500 * time.Millisecond):
	}

	assert.True(t, usedSIGKILL, "softKillProcess must escalate to SIGKILL when TERM is ignored")
	// Allow generous slack for scheduler jitter — the important
	// invariant is that it didn't hang for seconds.
	assert.Less(t, elapsed, 1500*time.Millisecond, "softKillProcess should return promptly after grace")

	// Confirm the child is actually dead.
	err := syscall.Kill(pid, 0)
	assert.True(t, errors.Is(err, syscall.ESRCH), "child process should be dead after SIGKILL; got err=%v", err)
}

// TestSoftKillProcess_AlreadyDeadIsNoop asserts that calling
// softKillProcess on a PID that is already gone returns false (no
// SIGKILL needed) and does not panic.
func TestSoftKillProcess_AlreadyDeadIsNoop(t *testing.T) {
	cmd := exec.Command("sh", "-c", "exit 0")
	require.NoError(t, cmd.Start())
	pid := cmd.Process.Pid
	_, _ = cmd.Process.Wait() // fully reap

	// Race: pid may be recycled on extremely fast systems, but for a
	// freshly-reaped pid within the same goroutine this is stable.
	usedSIGKILL := softKillProcess(pid, 100*time.Millisecond)
	assert.False(t, usedSIGKILL, "already-dead pid should not trigger SIGKILL")
}
