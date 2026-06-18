package tmux

// Regression coverage for the orphan control-client accumulation leak.
//
// killStaleControlClients only sweeps clients attached to ONE named session
// and only fires on PipeManager.Connect(). Orphaned `tmux -C attach-session`
// clients belonging to sessions the TUI never reconnects to therefore pile up
// indefinitely — each prior crashed/SIGKILL'd TUI leaves one orphan per
// session, and only the sessions actively reopened get cleaned. Observed in
// the wild as 176 orphaned `tmux -C` clients exhausting the macOS pty cap
// (kern.tty.ptmx_max=511), blocking every new tmux/terminal.
//
// SweepStaleControlClients reaps orphans across EVERY session on the server in
// a single call. It runs once at TUI startup so each launch clears the entire
// backlog left behind by previous dead TUIs.

import (
	"fmt"
	"os/exec"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// TestSweepStaleControlClients_AcrossAllSessions proves a single server-wide
// sweep reaps orphans on multiple distinct sessions. The per-session
// killStaleControlClients(sessionA) could never reap sessionB's orphan; this
// is exactly the gap that let 176 orphans accumulate.
func TestSweepStaleControlClients_AcrossAllSessions(t *testing.T) {
	nameA := createTestSessionStrict(t, "sweep-all-a")
	nameB := createTestSessionStrict(t, "sweep-all-b")

	staleA := spawnOrphanControlClient(t, nameA)
	staleB := spawnOrphanControlClient(t, nameB)

	clients := []struct {
		session string
		pid     int
	}{{nameA, staleA}, {nameB, staleB}}

	// Both orphans must register with tmux before the sweep.
	for _, c := range clients {
		require.Eventuallyf(t, func() bool {
			out, _ := exec.Command("tmux", "list-clients", "-t", c.session, "-F", "#{client_control_mode} #{client_pid}").Output()
			return strings.Contains(string(out), fmt.Sprintf("1 %d", c.pid))
		}, 3*time.Second, 100*time.Millisecond, "orphan on %s should register", c.session)
	}

	// One server-wide sweep reaps orphans on BOTH sessions.
	SweepStaleControlClients("")

	for _, c := range clients {
		require.Eventuallyf(t, func() bool {
			out, _ := exec.Command("tmux", "list-clients", "-t", c.session, "-F", "#{client_control_mode} #{client_pid}").Output()
			return !strings.Contains(string(out), fmt.Sprintf("1 %d", c.pid))
		}, 2*time.Second, 100*time.Millisecond, "orphan on %s should be reaped by the global sweep", c.session)
	}
}

// TestSweepStaleControlClients_PreservesLiveSibling carries the #927 guard
// through the server-wide sweep: a live sibling TUI's control client (parent
// alive and agent-deck-like) must survive a global sweep, exactly as it
// survives the per-session sweep.
func TestSweepStaleControlClients_PreservesLiveSibling(t *testing.T) {
	name := createTestSessionStrict(t, "sweep-live-sibling")

	siblingPipe, err := NewControlPipe(name, "")
	require.NoError(t, err)
	siblingPID := siblingPipe.cmd.Process.Pid
	t.Cleanup(func() { siblingPipe.Close() })

	require.Eventually(t, func() bool {
		out, _ := exec.Command("tmux", "list-clients", "-t", name, "-F", "#{client_control_mode} #{client_pid}").Output()
		return strings.Contains(string(out), fmt.Sprintf("1 %d", siblingPID))
	}, 3*time.Second, 100*time.Millisecond, "sibling control client should register")

	SweepStaleControlClients("")

	// Wait past the full SIGTERM→SIGKILL escalation window so an erroneous
	// kill would have landed by now.
	time.Sleep(controlClientKillGrace + 750*time.Millisecond)
	require.True(t, siblingPipe.IsAlive(),
		"live sibling control client must survive SweepStaleControlClients (#927)")
}
