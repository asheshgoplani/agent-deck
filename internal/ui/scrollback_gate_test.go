package ui

import (
	"errors"
	"testing"
)

// TestOpenScrollbackOnPageUp verifies the alternate-screen gate decision: a bare
// PageUp opens the pager on a normal-screen pane (there is real tmux scrollback),
// passes through on an alternate-screen pane (a full-screen app such as Claude
// fullscreen scrolls itself), and — on a query failure — preserves the pager
// rather than silently disabling a configured feature.
func TestOpenScrollbackOnPageUp(t *testing.T) {
	boom := errors.New("tmux query failed")
	tests := []struct {
		name string
		alt  bool
		err  error
		want bool
	}{
		{"normal screen opens pager", false, nil, true},
		{"alt screen passes pageup through", true, nil, false},
		{"query error preserves pager (normal)", false, boom, true},
		{"query error preserves pager (alt)", true, boom, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := openScrollbackOnPageUp(tt.alt, tt.err); got != tt.want {
				t.Fatalf("openScrollbackOnPageUp(%v, %v) = %v, want %v", tt.alt, tt.err, got, tt.want)
			}
		})
	}
}
