package tmux

import (
	"os/exec"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// tmuxCtl returns a helper that runs tmux against the test's isolated server
// and fails the test on any error.
func tmuxCtl(t *testing.T, socket string) func(args ...string) string {
	return func(args ...string) string {
		t.Helper()
		out, err := exec.Command("tmux", append([]string{"-L", socket}, args...)...).CombinedOutput()
		require.NoError(t, err, "%s", out)
		return strings.TrimSpace(string(out))
	}
}

// countAfterNewWindowHooks counts the after-new-window entries in a
// show-hooks listing, split into Deck's reserved slot and everything else.
func countAfterNewWindowHooks(hooks string) (deck, other int) {
	for _, line := range strings.Split(hooks, "\n") {
		line = strings.TrimSpace(line)
		switch {
		case strings.HasPrefix(line, "after-new-window[2259] "):
			deck++
		case strings.HasPrefix(line, "after-new-window"):
			other++
		}
	}
	return deck, other
}

// #2259: a window opened by hand inside a Deck session (tmux's own `c`
// binding, a control-mode client, anything other than Session.NewShellWindow)
// must pick up the same window sizing policy (window-size and
// aggressive-resize, defaults or [tmux.options] overrides) that Start() gives
// the initial window and NewShellWindow gives Deck-opened windows.
func TestSession_HandOpenedWindowInheritsWindowPolicy(t *testing.T) {
	requireTmux(t)
	for _, tc := range []struct {
		name              string
		overrides         map[string]string
		wantSize, wantAgg string
	}{
		{"defaults", nil, "smallest", "on"},
		{"overrides", map[string]string{"window-size": "largest", "aggressive-resize": "on"}, "largest", "on"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			socket, unrelated := makeIsolatedServer(t)
			ctl := tmuxCtl(t, socket)
			// Server-wide defaults differ from Deck's policy, so a window that
			// never had the policy applied reads back latest/off here.
			ctl("set-option", "-gw", "window-size", "latest")
			ctl("set-option", "-gw", "aggressive-resize", "off")

			s := NewSession("hand-opened", t.TempDir())
			s.SocketName = socket
			s.OptionOverrides = tc.overrides
			require.NoError(t, s.Start(""))

			// A user opening a window by hand: a bare `new-window`, not
			// Session.NewShellWindow.
			handID := ctl("new-window", "-P", "-F", "#{window_id}", "-t", s.Name)
			assert.Equal(t, tc.wantSize, ctl("show-options", "-wAv", "-t", handID, "window-size"), "window-size of hand-opened window")
			assert.Equal(t, tc.wantAgg, ctl("show-options", "-wAv", "-t", handID, "aggressive-resize"), "aggressive-resize of hand-opened window")

			// The hook is server-wide but must leave sessions Deck did not
			// start alone.
			otherID := ctl("new-window", "-P", "-F", "#{window_id}", "-t", unrelated)
			assert.Equal(t, "latest", ctl("show-options", "-wAv", "-t", otherID, "window-size"), "window-size in a non-Deck session")
			assert.Equal(t, "off", ctl("show-options", "-wAv", "-t", otherID, "aggressive-resize"), "aggressive-resize in a non-Deck session")
		})
	}
}

// A user's own global after-new-window hook must keep running inside Deck
// sessions (a session-scoped hook would shadow it: tmux resolves the hook
// array at the most specific scope only), and a value it installs wins over
// Deck's policy, as documented for #2186.
func TestSession_HandOpenedWindowKeepsUserGlobalHook(t *testing.T) {
	requireTmux(t)
	for _, tc := range []struct{ name, userHook, wantAgg, wantSize string }{
		{"unrelated user hook", "set-option -w @user_hook_ran yes", "on", "smallest"},
		{"user opts out of the policy", "set-option -w @user_hook_ran yes ; set-option -w aggressive-resize off ; set-option -w window-size manual", "off", "manual"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			socket, _ := makeIsolatedServer(t)
			ctl := tmuxCtl(t, socket)
			ctl("set-option", "-gw", "aggressive-resize", "off")
			ctl("set-hook", "-g", "after-new-window", tc.userHook)

			s := NewSession("user-hook", t.TempDir())
			s.SocketName = socket
			require.NoError(t, s.Start(""))

			handID := ctl("new-window", "-P", "-F", "#{window_id}", "-t", s.Name)
			assert.Equal(t, "yes", ctl("show-options", "-wqv", "-t", handID, "@user_hook_ran"), "the user's global after-new-window hook must still run in a Deck session")
			assert.Equal(t, tc.wantAgg, ctl("show-options", "-wAv", "-t", handID, "aggressive-resize"))
			assert.Equal(t, tc.wantSize, ctl("show-options", "-wAv", "-t", handID, "window-size"))

			hooks := ctl("show-hooks", "-g")
			deck, other := countAfterNewWindowHooks(hooks)
			assert.Equal(t, 1, deck, "Deck's hook entry:\n%s", hooks)
			assert.Equal(t, 1, other, "the user's hook entry must survive Start():\n%s", hooks)
		})
	}
}

// Deck's hook lives in one reserved slot of the server-wide array, so
// installing it any number of times (every Start() on the server, or the
// installer alone) leaves exactly one Deck entry and the user's entries.
func TestSession_WindowPolicyHookInstallIsIdempotent(t *testing.T) {
	requireTmux(t)
	socket, _ := makeIsolatedServer(t)
	ctl := tmuxCtl(t, socket)
	ctl("set-hook", "-g", "after-new-window", "set-option -w @user_hook_ran yes")

	for _, name := range []string{"first", "second"} {
		s := NewSession(name, t.TempDir())
		s.SocketName = socket
		require.NoError(t, s.Start(""))
	}
	// The installer on its own, twice more, against the same server.
	for range 2 {
		state, err := InstallWindowPolicyHook(socket)
		require.NoError(t, err)
		assert.Equal(t, WindowPolicyHookOwned, state)
	}

	hooks := ctl("show-hooks", "-g")
	deck, other := countAfterNewWindowHooks(hooks)
	assert.Equal(t, 1, deck, "repeated installs must not pile up Deck entries:\n%s", hooks)
	assert.Equal(t, 1, other, "the user's entry must survive repeated installs:\n%s", hooks)
}
