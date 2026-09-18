package tmux

import (
	"os/exec"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Review round 2, finding 1: a non-empty [tmux.options] override tmux rejects
// (a typo such as window-size = "biggest") was tolerated best-effort before
// the hook existed. Published to the hook verbatim it made the hook, and so
// the `new-window` command itself, fail on every window: NewShellWindow
// returned exit status 1 after creating the window (the duplicate-tab path
// #2186 exists to prevent) and every hand-opened window errored. Only values
// tmux accepts are published; the rest apply nothing.
func TestSession_InvalidOverrideDoesNotBreakNewWindows(t *testing.T) {
	requireTmux(t)
	socket, _ := makeIsolatedServer(t)
	ctl := tmuxCtl(t, socket)
	ctl("set-option", "-gw", "aggressive-resize", "off")

	s := NewSession("typo-override", t.TempDir())
	s.SocketName = socket
	s.OptionOverrides = map[string]string{"window-size": "biggest"}
	require.NoError(t, s.Start(""))

	assert.NoError(t, s.NewShellWindow(""), "an invalid override must not report the created window as a failure")
	windows := strings.Fields(ctl("list-windows", "-t", s.Name, "-F", "#{window_id}"))
	assert.Len(t, windows, 2, "exactly the initial window and the one NewShellWindow opened")

	// A hand-opened window: the bare `new-window -P` must exit 0, and the
	// hook must still apply the option whose value is valid.
	out, err := exec.Command("tmux", "-L", socket, "new-window", "-P", "-F", "#{window_id}", "-t", s.Name).CombinedOutput()
	require.NoError(t, err, "bare new-window must exit 0 with an invalid override configured: %s", out)
	handID := strings.TrimSpace(string(out))
	assert.Equal(t, "on", ctl("show-options", "-wAv", "-t", handID, "aggressive-resize"), "the valid default is still applied by the hook")
	assert.Empty(t, ctl("show-options", "-qv", "-t", s.Name, "@agentdeck_window_size"), "an invalid value is never published for the hook")
	assert.Equal(t, "on", ctl("show-options", "-qv", "-t", s.Name, "@agentdeck_aggressive_resize"))
}

// Review round 2, finding 2: a foreign entry at Deck's reserved index must
// win, as it does for terminal-features[2147483647]; an entry of our own
// (this or an older release's) is refreshed.
func TestSession_WindowPolicyHookRespectsForeignSlot(t *testing.T) {
	requireTmux(t)
	const foreign = "set-option -w @user_slot_2259 yes"
	const older = "if-shell -F '#{@agentdeck_window_size}' 'set-option -w window-size latest'"
	for _, tc := range []struct {
		name, before string
		wantPolicy   bool // Deck's hook is in the slot afterwards
	}{
		{"foreign entry is kept", foreign, false},
		{"older Deck entry is refreshed", older, true},
		{"empty slot is installed", "", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			socket, _ := makeIsolatedServer(t)
			ctl := tmuxCtl(t, socket)
			ctl("set-option", "-gw", "aggressive-resize", "off")
			ctl("set-hook", "-g", afterNewWindowHookSlot, tc.before)

			s := NewSession("slot-owner", t.TempDir())
			s.SocketName = socket
			require.NoError(t, s.Start(""))

			// tmux re-serialises a stored hook, so compare by content, not
			// byte for byte: Deck's current hook reads both options.
			content := ctl("show-options", "-gqv", afterNewWindowHookSlot)
			handID := ctl("new-window", "-P", "-F", "#{window_id}", "-t", s.Name)
			if tc.wantPolicy {
				assert.Contains(t, content, "@agentdeck_aggressive_resize", "slot must hold the current hook")
				assert.Equal(t, "on", ctl("show-options", "-wAv", "-t", handID, "aggressive-resize"))
			} else {
				assert.Equal(t, foreign, content, "the foreign entry must be untouched")
				assert.Equal(t, "yes", ctl("show-options", "-wqv", "-t", handID, "@user_slot_2259"), "the foreign hook must keep running")
				assert.Equal(t, "off", ctl("show-options", "-wAv", "-t", handID, "aggressive-resize"), "Deck's policy is not applied when it could not install its hook")
			}
		})
	}
}
