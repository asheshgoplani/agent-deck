//go:build !windows

package session

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"testing"

	"github.com/asheshgoplani/agent-deck/internal/tmux"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestViewers_StoppedSessionIsNobodyOnEveryPath: a session whose tmux
// session does not exist (stopped, or its whole server gone) has no
// viewers, and `session viewers` (Instance.Viewers) and `list --json`
// (ViewersByTmuxSession) both say so as "nobody", never "unknown".
func TestViewers_StoppedSessionIsNobodyOnEveryPath(t *testing.T) {
	skipIfNoTmuxBinary(t)
	ctx := context.Background()

	// A socket nobody ever started a server on.
	dead := NewInstance("viewers-dead-socket", "/tmp")
	dead.tmuxSession = tmux.NewSession(dead.Title, "/tmp")
	dead.tmuxSession.SocketName = fmt.Sprintf("ad-viewers-dead-%d", os.Getpid())

	// A live server that does not have this session.
	live := NewInstance("viewers-live", "/tmp")
	live.tmuxSession = tmux.NewSession(live.Title, "/tmp")
	live.tmuxSession.SocketName = fmt.Sprintf("ad-viewers-live-%d", os.Getpid())
	t.Cleanup(func() { _ = exec.Command("tmux", "-L", live.tmuxSession.SocketName, "kill-server").Run() })
	require.NoError(t, live.tmuxSession.Start("sleep 3600"))
	stopped := NewInstance("viewers-stopped", "/tmp")
	stopped.tmuxSession = tmux.NewSession(stopped.Title, "/tmp")
	stopped.tmuxSession.SocketName = live.tmuxSession.SocketName

	// No tmux session object at all: nothing to ask about.
	never := NewInstance("viewers-never", "/tmp")
	never.tmuxSession = nil

	for _, inst := range []*Instance{dead, stopped} {
		viewers, known := inst.Viewers(ctx)
		assert.True(t, known, "%s: session viewers must answer, not say unknown", inst.Title)
		assert.NotNil(t, viewers, "%s: encodes as []", inst.Title)
		assert.Empty(t, viewers)
	}
	_, known := never.Viewers(ctx)
	assert.False(t, known, "no tmux session to ask about")

	byName := ViewersByTmuxSession(ctx, []*Instance{dead, live, stopped, never})
	for _, inst := range []*Instance{dead, live, stopped} {
		viewers, ok := byName[inst.tmuxSession.Name]
		assert.True(t, ok, "%s: list --json carries viewers", inst.Title)
		assert.NotNil(t, viewers)
		assert.Empty(t, viewers)
	}
	assert.Len(t, byName, 3, "an instance without a tmux session is absent (unknown)")
}
