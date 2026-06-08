// Regression test for issue #965 — orphaned MCP child processes accumulate
// with PPID=1 because session stop doesn't reap them.
//
// When a session is stopped, any stdio MCP children whose PIDs are tracked
// on the session record must receive SIGTERM (with grace) and SIGKILL if
// still alive. Without this, the children get reparented to PID 1 and leak.
package session

import (
	"errors"
	"os/exec"
	"runtime"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"
)

// TestSessionStop_ReapsMcpChildren_RegressionFor965 verifies that calling
// Kill() on an Instance terminates any MCP child PIDs registered on it.
//
// The test uses a hermetic fake MCP child: a long-running `sleep` process
// owned by the test. It registers the PID via RegisterMCPChild and then
// asserts that the PID is dead/gone after Kill().
func TestSessionStop_ReapsMcpChildren_RegressionFor965(t *testing.T) {
	if runtime.GOOS != "linux" && runtime.GOOS != "darwin" {
		t.Skipf("unix-only: this test relies on syscall.Kill semantics")
	}

	// Spawn a fake MCP child: sleep 120s. The test must reap it itself
	// (Wait) — Kill() in production is responsible for sending the
	// terminating signal, but the test's exec.Cmd is the parent.
	cmd := exec.Command("sleep", "120")
	if err := cmd.Start(); err != nil {
		t.Fatalf("spawn fake MCP child: %v", err)
	}
	pid := cmd.Process.Pid
	t.Cleanup(func() {
		// Belt-and-suspenders: if the production code failed to kill
		// the child, the test must still reap it so we don't leak.
		_ = cmd.Process.Kill()
		_, _ = cmd.Process.Wait()
	})

	inst := &Instance{ID: "test-965", Title: "issue965"}
	inst.RegisterMCPChild(pid)

	if err := inst.Kill(); err != nil {
		t.Fatalf("Instance.Kill: %v", err)
	}

	// After Kill(), the child must be waitable within a short window. This test
	// owns the fake child process, so waiting is the portable way to distinguish
	// a successfully-signaled zombie from a still-running process on macOS.
	if err := waitCmdExited(cmd, 5*time.Second); err != nil {
		t.Fatalf("fake MCP child PID %d still alive after Instance.Kill — orphan reap regression for #965: %v", pid, err)
	}
}

func waitCmdExited(cmd *exec.Cmd, within time.Duration) error {
	done := make(chan error, 1)
	go func() {
		_, err := cmd.Process.Wait()
		done <- err
	}()
	select {
	case err := <-done:
		if err == nil || errors.Is(err, syscall.ECHILD) {
			return nil
		}
		return err
	case <-time.After(within):
		return errors.New("timed out waiting for command exit")
	}
}

// waitChildDead polls until syscall.Kill(pid, 0) returns ESRCH or the process
// is in a terminal zombie/exiting state. It is for children owned by tmux/init,
// where the Go test process cannot call Wait.
func waitChildDead(t *testing.T, pid int, within time.Duration) bool {
	t.Helper()
	deadline := time.Now().Add(within)
	for time.Now().Before(deadline) {
		if err := syscall.Kill(pid, syscall.Signal(0)); err != nil {
			return true
		}
		if childIsZombieOrExiting(pid) {
			return true
		}
		time.Sleep(25 * time.Millisecond)
	}
	return false
}

func TestChildIsZombieOrExitingDoesNotTreatPsLookupFailureAsTerminal(t *testing.T) {
	if childIsZombieOrExiting(99999999) {
		t.Fatal("nonexistent PID was classified as zombie/exiting")
	}
}

func childIsZombieOrExiting(pid int) bool {
	out, err := exec.Command("ps", "-o", "state=", "-p", strconv.Itoa(pid)).Output()
	if err != nil {
		return false
	}
	state := strings.TrimSpace(string(out))
	return strings.HasPrefix(state, "Z") || strings.HasPrefix(state, "X")
}
