//go:build !windows
// +build !windows

package tmux

import "testing"

func TestAttachStdinPump_DropsColorReplyForOldTmux(t *testing.T) {
	pump, w, out := newTestPump(t, AttachOptions{DetachByte: 17})
	pump.tmuxIgnoresColorReplies = true

	if got := tmuxInputFromArmedPump(t, pump, w, out, hostColorReplies); got != "" {
		t.Errorf("pane input = %q, want nothing", got)
	}
}

func TestTmuxParsesColorReplies(t *testing.T) {
	for _, tc := range []struct {
		raw  string
		want bool
	}{
		{"tmux 3.3a", false},
		{"tmux 3.2", false},
		{"tmux 2.9a", false},
		{"tmux 3.4", true},
		{"tmux 3.5a", true},
		{"tmux 3.6a", true},
		{"tmux 4.0", true},
		{"tmux master", true},
		{"tmux next-3.7", true},
		{"", true},
	} {
		t.Run(tc.raw, func(t *testing.T) {
			if got := tmuxParsesColorReplies(parseTmuxVersion(tc.raw)); got != tc.want {
				t.Errorf("tmuxParsesColorReplies(%q) = %v, want %v", tc.raw, got, tc.want)
			}
		})
	}
}
