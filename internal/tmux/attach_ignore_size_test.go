package tmux

import "testing"

// attach-session -f came in tmux 3.2. A phone web terminal on an older or
// unknown tmux must attach without it, or tmux rejects the attach.
func TestTmuxSupportsAttachIgnoreSize(t *testing.T) {
	for ver, want := range map[string]bool{
		"":       false,
		"3.0a":   false,
		"3.1c":   false,
		"3.2":    true,
		"3.6":    true,
		"master": false,
	} {
		if got := tmuxSupportsAttachIgnoreSize(ver); got != want {
			t.Errorf("tmuxSupportsAttachIgnoreSize(%q) = %v, want %v", ver, got, want)
		}
	}
}
