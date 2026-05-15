// Regression test for issue #965 — orphaned MCP child processes accumulate
// with PPID=1 because session stop doesn't reap them.
//
// When a session is stopped, any stdio MCP children whose PIDs are tracked
// on the session record must receive SIGTERM (with grace) and SIGKILL if
// still alive. Without this, the children get reparented to PID 1 and leak.
package session

import (
	"os"
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

	// Sanity: the child is alive before stop.
	if err := syscall.Kill(pid, syscall.Signal(0)); err != nil {
		t.Fatalf("fake MCP child PID %d not alive after Start: %v", pid, err)
	}

	inst := &Instance{ID: "test-965", Title: "issue965"}
	inst.RegisterMCPChild(pid)

	if err := inst.Kill(); err != nil {
		t.Fatalf("Instance.Kill: %v", err)
	}

	// After Kill(), the child must be dead within a short window.
	// Acceptable terminal states: ESRCH (gone), zombie ('Z'), exiting ('X').
	if !waitChildDead(t, pid, 5*time.Second) {
		t.Fatalf("fake MCP child PID %d still alive after Instance.Kill — orphan reap regression for #965", pid)
	}

	// Reap the zombie if any so cleanup is clean.
	_, _ = cmd.Process.Wait()
}

// waitChildDead polls until syscall.Kill(pid, 0) returns ESRCH (or the
// process is in a zombie state). Returns true on death within the deadline.
func waitChildDead(t *testing.T, pid int, within time.Duration) bool {
	t.Helper()
	deadline := time.Now().Add(within)
	for time.Now().Before(deadline) {
		if err := syscall.Kill(pid, syscall.Signal(0)); err != nil {
			// ESRCH or EPERM → not addressable as a live process.
			return true
		}
		// On Linux, a zombie still responds to Kill(0). Probe /proc.
		if runtime.GOOS == "linux" {
			data, rerr := os.ReadFile("/proc/" + strconv.Itoa(pid) + "/status")
			if rerr != nil {
				return true
			}
			for _, line := range strings.Split(string(data), "\n") {
				if strings.HasPrefix(line, "State:") {
					fields := strings.Fields(line)
					if len(fields) >= 2 && (fields[1] == "Z" || fields[1] == "X") {
						return true
					}
					break
				}
			}
		}
		time.Sleep(25 * time.Millisecond)
	}
	return false
}
