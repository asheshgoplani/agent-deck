package ui

import (
	"bytes"
	"strings"
	"testing"
)

// A panicking TUI left mouse reporting on in the owner's terminal: the reset
// must turn off every mode the TUI or an embedded tmux client can enable.
func TestResetTerminalAfterAbort(t *testing.T) {
	var buf bytes.Buffer
	ResetTerminalAfterAbort(&buf)
	out := buf.String()
	for _, seq := range []string{
		"\x1b[?1000l", "\x1b[?1002l", "\x1b[?1003l", "\x1b[?1005l", "\x1b[?1006l", "\x1b[?1015l",
		"\x1b[?1004l", "\x1b[?2004l", "\x1b[>4;0m", "\x1b[<u", "\x1b[?25h", "\x1b[?1049l",
	} {
		if !strings.Contains(out, seq) {
			t.Errorf("reset misses %q", seq)
		}
	}
	if strings.Index(out, "\x1b[?1049l") < strings.Index(out, "\x1b[?1006l") {
		t.Error("leave the alternate screen last, after the modes are off")
	}
}
