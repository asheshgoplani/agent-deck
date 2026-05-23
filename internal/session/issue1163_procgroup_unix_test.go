//go:build linux || darwin

// Issue #1163 Change 3 — process-group reaping.
//
// EVIDENCE-plugin.md proved the bun telegram poller outlives its parent
// because nothing signals it: it sits in the parent's process group and a
// kill of just the leader leaves the whole subtree (bun wrapper + bun
// server) orphaned. The structural fix is to spawn each managed child in
// its own process group (Setpgid) and, on stop, signal the negative pgid so
// the entire tree dies regardless of how the leader exits. This test locks
// in that mechanism — the same one socket_proxy.go already uses and that
// http_server.go now adopts.
package session

import (
	"fmt"
	"os/exec"
	"syscall"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func processAlive(pid int) bool {
	// signal 0 probes existence without delivering a signal.
	return syscall.Kill(pid, 0) == nil
}

func TestStop_KillsProcessGroup_ReapsGrandchild(t *testing.T) {
	// Parent shell that spawns a long-lived grandchild and prints its pid.
	cmd := exec.Command("bash", "-c", "sleep 1000 & echo $! ; wait")
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}

	out, err := cmd.StdoutPipe()
	require.NoError(t, err)
	require.NoError(t, cmd.Start())

	var gcPid int
	_, err = fmt.Fscanln(out, &gcPid)
	require.NoError(t, err, "must read grandchild pid from parent stdout")
	require.Greater(t, gcPid, 1, "grandchild pid must be a real process")

	// Grandchild is alive.
	require.True(t, processAlive(gcPid), "grandchild should be alive before kill")

	// Kill the WHOLE process group (negative pid), not just the leader.
	pgid, err := syscall.Getpgid(cmd.Process.Pid)
	require.NoError(t, err)
	require.NoError(t, syscall.Kill(-pgid, syscall.SIGKILL))
	_ = cmd.Wait()

	// The grandchild must be reaped along with the group, not orphaned.
	deadline := time.Now().Add(2 * time.Second)
	for processAlive(gcPid) && time.Now().Before(deadline) {
		time.Sleep(20 * time.Millisecond)
	}
	assert.False(t, processAlive(gcPid),
		"grandchild must be reaped with the process group (it would leak if only the leader were killed)")
}

// Contrast case: killing ONLY the leader leaves the grandchild orphaned —
// this is exactly the leak EVIDENCE-plugin.md documented, proving the
// group kill above is load-bearing.
func TestStop_KillingOnlyLeader_OrphansGrandchild(t *testing.T) {
	cmd := exec.Command("bash", "-c", "sleep 1000 & echo $! ; wait")
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}

	out, err := cmd.StdoutPipe()
	require.NoError(t, err)
	require.NoError(t, cmd.Start())

	var gcPid int
	_, err = fmt.Fscanln(out, &gcPid)
	require.NoError(t, err)
	require.True(t, processAlive(gcPid))

	// Kill only the leader (positive pid).
	require.NoError(t, cmd.Process.Kill())
	_ = cmd.Wait()

	time.Sleep(200 * time.Millisecond)
	assert.True(t, processAlive(gcPid),
		"killing only the leader leaves the grandchild alive — the bug we are guarding against")

	// Clean up the orphan we deliberately created.
	_ = syscall.Kill(gcPid, syscall.SIGKILL)
}
