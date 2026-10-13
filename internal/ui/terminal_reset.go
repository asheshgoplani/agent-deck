package ui

import "io"

// terminalResetSequence turns off every terminal mode the TUI or its
// embedded tmux client may have turned on: all mouse reporting modes and
// encodings (Bubble Tea's own restore covers only 1002, 1003 and 1006, and an
// embedded client can enable 1000), focus reporting, bracketed paste,
// modifyOtherKeys and the kitty keyboard flags pushed at startup; then it
// shows the cursor and leaves the alternate screen.
const terminalResetSequence = "\x1b[?1000l\x1b[?1002l\x1b[?1003l\x1b[?1005l\x1b[?1006l\x1b[?1015l" +
	"\x1b[?1004l\x1b[?2004l" +
	"\x1b[>4;0m\x1b[<u" +
	"\x1b[?25h\x1b[?1049l"

// ResetTerminalAfterAbort writes terminalResetSequence to the real terminal
// (not the Bubble Tea output wrapper). Call it on every exit that skips the
// normal shutdown: a panic or kill of the Bubble Tea program, or a signal
// handler that ends in os.Exit. Without it a crashed TUI leaves mouse
// reporting on and the shell prompt fills with mouse escape codes.
func ResetTerminalAfterAbort(w io.Writer) {
	_, _ = io.WriteString(w, terminalResetSequence)
}
