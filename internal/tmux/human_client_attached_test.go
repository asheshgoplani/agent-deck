package tmux

import (
	"os/exec"
	"testing"
	"time"

	"github.com/creack/pty"
	"github.com/stretchr/testify/require"
)

// TestHumanClientAttached_IgnoresControlModeClients_Integration (ASK-119):
// agent-deck's own control-mode clients (live pipes, key senders) raise
// #{session_attached}, so they must not count as a person at the pane; a
// real terminal client must.
func TestHumanClientAttached_IgnoresControlModeClients_Integration(t *testing.T) {
	s := newSharedViewSession(t, "humanclient")

	attached, err := s.HumanClientAttached()
	require.NoError(t, err)
	require.False(t, attached, "no client at all")

	// A control-mode client, as PipeManager and KeySender hold. Its stdin
	// is a pty kept open so tmux does not drop it at once.
	ctl := exec.Command("tmux", "-L", s.SocketName, "-C", "attach-session", "-t", s.Name)
	ptmx, err := pty.Start(ctl)
	require.NoError(t, err)
	t.Cleanup(func() {
		_ = ptmx.Close()
		_ = ctl.Process.Kill()
		_, _ = ctl.Process.Wait()
	})
	go func() {
		buf := make([]byte, 4096)
		for {
			if _, err := ptmx.Read(buf); err != nil {
				return
			}
		}
	}()
	require.Eventually(t, func() bool {
		out, err := s.tmuxCmd("display-message", "-p", "-t", s.Name, "#{session_attached}").Output()
		return err == nil && string(out) == "1\n"
	}, 5*time.Second, 50*time.Millisecond, "control client counted by session_attached")

	attached, err = s.HumanClientAttached()
	require.NoError(t, err)
	require.False(t, attached, "a control-mode client is not a person")

	attachSharedViewClient(t, s.SocketName, s.Name, 120, 40)
	require.Eventually(t, func() bool {
		a, err := s.HumanClientAttached()
		return err == nil && a
	}, 5*time.Second, 50*time.Millisecond, "a terminal client is a person")
}

func TestHasHumanClient(t *testing.T) {
	for _, tc := range []struct {
		out  string
		want bool
	}{
		{"", false},
		{"1\n", false},
		{"1\n1\n", false},
		{"0\n", true},
		{"1\n0\n", true},
		{"\n", false},
	} {
		require.Equal(t, tc.want, hasHumanClient(tc.out), "%q", tc.out)
	}
}
