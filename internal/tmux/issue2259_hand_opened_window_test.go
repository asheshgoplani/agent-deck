package tmux

import (
	"os/exec"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

// #2259: a window opened by hand inside a Deck session (tmux's own `c`
// binding, or any path other than Session.NewShellWindow) must still pick
// up the aggressive-resize policy Deck applies to its own windows (#2186).
// tmux offers no way to retarget a per-window option after the window
// already exists, but a session-scoped after-new-window hook, installed
// once in Start(), reaches every later window regardless of how it was
// created.
func TestSession_HandOpenedWindowInheritsAggressiveResize(t *testing.T) {
	requireTmux(t)
	socket, _ := makeIsolatedServer(t)
	ctl := func(args ...string) string {
		t.Helper()
		out, err := exec.Command("tmux", append([]string{"-L", socket}, args...)...).CombinedOutput()
		require.NoError(t, err, "%s", out)
		return strings.TrimSpace(string(out))
	}
	// Server-wide default is off, so a window that never got Deck's setting
	// applied would read back "off" here.
	ctl("set-option", "-gw", "aggressive-resize", "off")

	s := NewSession("hand-opened", t.TempDir())
	s.SocketName = socket
	require.NoError(t, s.Start(""))

	// Simulate a user opening a window by hand: a bare `new-window`, not
	// Session.NewShellWindow.
	handID := ctl("new-window", "-P", "-F", "#{window_id}", "-t", s.Name)

	got := ctl("show-options", "-wAv", "-t", handID, "aggressive-resize")
	require.Equal(t, "on", got, "hand-opened window %s must inherit aggressive-resize from the after-new-window hook", handID)
}

// The hook must be re-set, not appended, on repeated Start() calls against
// the same session so it doesn't accumulate duplicate entries.
func TestSession_StartDoesNotDuplicateAfterNewWindowHook(t *testing.T) {
	requireTmux(t)
	socket, _ := makeIsolatedServer(t)
	ctl := func(args ...string) string {
		t.Helper()
		out, err := exec.Command("tmux", append([]string{"-L", socket}, args...)...).CombinedOutput()
		require.NoError(t, err, "%s", out)
		return strings.TrimSpace(string(out))
	}

	s := NewSession("hook-idempotent", t.TempDir())
	s.SocketName = socket
	require.NoError(t, s.Start(""))
	require.NoError(t, s.Start(""))

	hooks := ctl("show-hooks", "-t", s.Name)
	count := 0
	for _, line := range strings.Split(hooks, "\n") {
		if strings.HasPrefix(strings.TrimSpace(line), "after-new-window") {
			count++
		}
	}
	require.Equal(t, 1, count, "Start() run twice must not pile up duplicate after-new-window hook entries:\n%s", hooks)
}
