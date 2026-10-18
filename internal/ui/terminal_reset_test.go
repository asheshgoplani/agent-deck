package ui

import (
	"bytes"
	"strings"
	"testing"
)

var terminalModesOff = []string{
	"\x1b[?1000l", "\x1b[?1002l", "\x1b[?1003l", "\x1b[?1005l", "\x1b[?1006l", "\x1b[?1015l",
	"\x1b[?1004l", "\x1b[?2004l", "\x1b[>4;0m", "\x1b[<u", "\x1b[?25h",
}

// After a Run error Bubble Tea has left the alternate screen and printed
// the panic: leaving it again restores the saved cursor and the next output
// overwrites the panic header, so the reset must not send ?1049l.
func TestResetTerminalAfterRunErrorKeepsTheMainScreen(t *testing.T) {
	var buf bytes.Buffer
	ResetTerminalAfterRunError(&buf)
	out := buf.String()
	for _, seq := range terminalModesOff {
		if !strings.Contains(out, seq) {
			t.Errorf("reset misses %q", seq)
		}
	}
	if strings.Contains(out, "\x1b[?1049l") {
		t.Errorf("Run-error reset leaves the alternate screen (restores the cursor over the panic): %q", out)
	}
}

// A signal arrives while the TUI still holds the alternate screen.
func TestResetTerminalOnSignalLeavesTheAltScreenLast(t *testing.T) {
	var buf bytes.Buffer
	ResetTerminalOnSignal(&buf)
	out := buf.String()
	for _, seq := range terminalModesOff {
		if !strings.Contains(out, seq) {
			t.Errorf("reset misses %q", seq)
		}
	}
	if !strings.HasSuffix(out, "\x1b[?1049l") {
		t.Errorf("signal reset must leave the alternate screen last, after the modes are off: %q", out)
	}
}
