package tmux

import (
	"testing"
	"time"
)

// tmux reads an argument ending in ';' as a command separator even with
// send-keys -l, so "/foo a;" typed "/foo a" (review L4, tmux 3.4). SendKeys
// must deliver the text literally.
func TestSendKeys_TrailingSemicolonIsTypedLiterally(t *testing.T) {
	requireTmux(t)
	socket, target := makeIsolatedServer(t)
	s := &Session{Name: target, SocketName: socket}
	for _, text := range []string{"Q7 a;", `Q8 b\;`} {
		if err := s.SendKeys(text); err != nil {
			t.Fatalf("SendKeys(%q): %v", text, err)
		}
		pollUntilPaneContains(t, socket, target, text, 2*time.Second)
	}
}

func TestEscapeTrailingSemicolon(t *testing.T) {
	for in, want := range map[string]string{
		"/foo a;": `/foo a\;`,
		"a;b":     "a;b",
		";":       `\;`,
		"plain":   "plain",
		`a\;`:     `a\\;`,
	} {
		if got := escapeTrailingSemicolon(in); got != want {
			t.Errorf("escapeTrailingSemicolon(%q) = %q, want %q", in, got, want)
		}
	}
}
