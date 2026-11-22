package web

import (
	"strings"
	"testing"
)

// The TMUX_SESSION_NOT_FOUND banner must point at controls that exist on
// every web layout. It used to say "Restart it from the sidebar (Restart
// icon, or press 'r' with the row focused)": phones have no sidebar, and `r`
// renames a session, it never restarts one.
func TestTmuxSessionNotFoundHintNamesRealRestartControls(t *testing.T) {
	h := tmuxSessionNotFoundHint
	for _, wrong := range []string{"sidebar", "press 'r'", "no longer exists"} {
		if strings.Contains(h, wrong) {
			t.Errorf("hint %q still says %q", h, wrong)
		}
	}
	if !strings.Contains(h, "Restart session") {
		t.Errorf("hint %q does not name the banner's Restart session button", h)
	}
}
