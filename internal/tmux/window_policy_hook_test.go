package tmux

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// `agent-deck tmux-hooks uninstall` removes the reserved slot only when it
// holds Deck's hook; status reports what is there without writing.
func TestWindowPolicyHookUninstallRemovesOnlyOwnedSlot(t *testing.T) {
	requireTmux(t)
	const foreign = "set-option -w @user_slot_2259 yes"
	for _, tc := range []struct {
		name, before string
		wantBefore   WindowPolicyHookState
		wantAfter    string // slot content after uninstall (tmux re-serialises; foreign is simple enough to round-trip)
	}{
		{"owned", windowPolicyHook(), WindowPolicyHookOwned, ""},
		{"foreign", foreign, WindowPolicyHookForeign, foreign},
		{"absent", "", WindowPolicyHookAbsent, ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			socket, _ := makeIsolatedServer(t)
			ctl := tmuxCtl(t, socket)
			ctl("set-hook", "-g", "after-new-window[0]", "set-option -w @user_hook_ran yes")
			if tc.before != "" {
				ctl("set-hook", "-g", afterNewWindowHookSlot, tc.before)
			}

			status, err := WindowPolicyHookStatus(socket)
			require.NoError(t, err)
			assert.Equal(t, tc.wantBefore, status)

			state, err := UninstallWindowPolicyHook(socket)
			require.NoError(t, err)
			assert.Equal(t, tc.wantBefore, state)
			assert.Equal(t, tc.wantAfter, ctl("show-options", "-gqv", afterNewWindowHookSlot))

			// countAfterNewWindowHooks counts by index, so a foreign occupant
			// of the slot shows up as the "deck" entry: it must survive.
			hooks := ctl("show-hooks", "-g")
			slot, other := countAfterNewWindowHooks(hooks)
			wantSlot := 0
			if tc.wantAfter != "" {
				wantSlot = 1
			}
			assert.Equal(t, wantSlot, slot, "reserved slot after uninstall:\n%s", hooks)
			assert.Equal(t, 1, other, "the user's index-0 entry must survive:\n%s", hooks)

			// The second uninstall finds nothing of ours and changes nothing.
			state, err = UninstallWindowPolicyHook(socket)
			require.NoError(t, err)
			assert.NotEqual(t, WindowPolicyHookOwned, state)
			assert.Equal(t, tc.wantAfter, ctl("show-options", "-gqv", afterNewWindowHookSlot))
		})
	}
}

func TestWindowPolicyHookStatusWithoutServer(t *testing.T) {
	requireTmux(t)
	_, err := WindowPolicyHookStatus("no-such-server-for-window-policy")
	assert.Error(t, err, "an unreadable slot is an error, never absent")
}

// Hooks became array options in tmux 3.0; older servers have no indexed
// slot, and versions tmux -V prints without a plain number are attempted.
func TestTmuxSupportsHookArrays(t *testing.T) {
	for _, tc := range []struct {
		raw  string
		want bool
	}{
		{"tmux 2.9a", false},
		{"tmux 2.8", false},
		{"tmux 1.9", false},
		{"tmux 3.0", true},
		{"tmux 3.0a", true},
		{"tmux 3.1c", true},
		{"tmux 3.2", true},
		{"tmux 3.5a", true},
		{"tmux 3.7b", true},
		{"tmux 4.0", true},
		{"tmux master", true},
		{"tmux next-3.8", true},
		{"tmux openbsd-7.4", true},
		{"", true},
		{"garbage", true},
	} {
		t.Run(tc.raw, func(t *testing.T) {
			assert.Equal(t, tc.want, tmuxSupportsHookArrays(parseTmuxVersion(tc.raw)))
		})
	}
}
