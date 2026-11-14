package session

import (
	"fmt"
	"os/exec"
	"testing"
	"time"

	"github.com/creack/pty"
	"github.com/stretchr/testify/require"

	"github.com/asheshgoplani/agent-deck/internal/tmux"
)

// The production attach check (ASK-119) must not count agent-deck's own
// control-mode clients (live pipes, key senders) as a person at the prompt,
// or a focused session would never be timed; a terminal client must count.
func TestWaitTimerAttached_ControlClientIsNotAPerson_Integration(t *testing.T) {
	if _, err := exec.LookPath("tmux"); err != nil {
		t.Skip("tmux not available")
	}
	inst := NewInstance("test-wait-attach", t.TempDir())
	ts := tmux.NewSession(inst.Title, t.TempDir())
	ts.SocketName = fmt.Sprintf("ad-wait-attach-%d", time.Now().UnixNano())
	t.Cleanup(func() { _ = exec.Command("tmux", "-L", ts.SocketName, "kill-server").Run() })
	require.NoError(t, ts.Start("sleep 3600"))
	inst.tmuxSession = ts

	attachClient := func(args ...string) {
		cmd := exec.Command("tmux", append([]string{"-L", ts.SocketName}, args...)...)
		cmd.Env = append(cmd.Environ(), "TERM=xterm-256color")
		ptmx, err := pty.StartWithSize(cmd, &pty.Winsize{Cols: 120, Rows: 40})
		require.NoError(t, err)
		t.Cleanup(func() {
			_ = ptmx.Close()
			_ = cmd.Process.Kill()
			_, _ = cmd.Process.Wait()
		})
		go func() {
			buf := make([]byte, 4096)
			for {
				if _, err := ptmx.Read(buf); err != nil {
					return
				}
			}
		}()
	}
	clients := func() int {
		out, err := exec.Command("tmux", "-L", ts.SocketName, "list-clients", "-t", ts.Name).Output()
		if err != nil || len(out) == 0 {
			return 0
		}
		n := 0
		for _, b := range out {
			if b == '\n' {
				n++
			}
		}
		return n
	}

	attachClient("-C", "attach-session", "-t", ts.Name)
	require.Eventually(t, func() bool { return clients() == 1 }, 5*time.Second, 50*time.Millisecond)
	attached, err := waitTimerAttached(inst)
	require.NoError(t, err)
	require.False(t, attached, "a control-mode client is not a person")

	attachClient("attach-session", "-t", ts.Name)
	require.Eventually(t, func() bool {
		a, err := waitTimerAttached(inst)
		return err == nil && a
	}, 5*time.Second, 50*time.Millisecond, "a terminal client is a person")
}
