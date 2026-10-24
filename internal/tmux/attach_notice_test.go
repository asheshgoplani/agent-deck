package tmux

import (
	"context"
	"os/exec"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestShowClientNotice_FollowsSwitchedClient_Integration: the attach notice
// finds the attached client by its pid even after the notification bar
// switched it to another session, and goes to that client only.
func TestShowClientNotice_FollowsSwitchedClient_Integration(t *testing.T) {
	s := newSharedViewSession(t, "attachnotice")
	ours := attachSharedViewClient(t, s.SocketName, s.Name, 120, 40)
	attachSharedViewClient(t, s.SocketName, s.Name, 200, 60)
	require.NoError(t, exec.Command("tmux", "-L", s.SocketName, "new-session", "-d", "-s", "other", "sleep 3600").Run())

	clientName := func() string {
		out, err := s.tmuxCmd("list-clients", "-F", "#{client_pid}\t#{client_name}").Output()
		require.NoError(t, err)
		for _, line := range strings.Split(strings.TrimSpace(string(out)), "\n") {
			if p, name, ok := strings.Cut(line, "\t"); ok && p == itoa(ours.cmd.Process.Pid) {
				return name
			}
		}
		return ""
	}
	var name string
	require.Eventually(t, func() bool { name = clientName(); return name != "" }, 5*time.Second, 50*time.Millisecond)

	// The notification bar's switch-client, as in a long attach.
	require.NoError(t, s.tmuxCmd("switch-client", "-c", name, "-t", "other").Run())
	require.Eventually(t, func() bool {
		out, err := s.tmuxCmd("display-message", "-p", "-c", name, "#{client_session}").Output()
		return err == nil && strings.TrimSpace(string(out)) == "other"
	}, 5*time.Second, 50*time.Millisecond)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	s.showClientNotice(ctx, ours.cmd.Process.Pid, "update waits: detach to restart")

	out, err := s.tmuxCmd("show-messages").Output()
	require.NoError(t, err)
	log := string(out)
	assert.Contains(t, log, name+" message: update waits: detach to restart",
		"the switched client still gets the notice")
	assert.Equal(t, 1, strings.Count(log, "message: update waits"), "no other client gets it")
}
