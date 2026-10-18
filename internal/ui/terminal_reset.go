package ui

import "io"

// terminalModesOffSequence turns off every terminal mode the TUI or its
// embedded tmux client may have turned on: all mouse reporting modes and
// encodings (Bubble Tea's own restore covers only 1002, 1003 and 1006, and an
// embedded client can enable 1000), focus reporting, bracketed paste,
// modifyOtherKeys, and any kitty keyboard flags left pushed (an attached
// tmux with extended-keys can push them; the TUI itself pops the stack at
// startup and on exit); then it shows the cursor.
const terminalModesOffSequence = "\x1b[?1000l\x1b[?1002l\x1b[?1003l\x1b[?1005l\x1b[?1006l\x1b[?1015l" +
	"\x1b[?1004l\x1b[?2004l" +
	"\x1b[>4;0m\x1b[<u" +
	"\x1b[?25h"

// leaveAltScreenSequence leaves the alternate screen. Terminals (xterm, VTE,
// tmux) also restore the saved cursor on it, even when already on the main
// screen, so it must only be sent while the TUI may still be on the
// alternate screen.
const leaveAltScreenSequence = "\x1b[?1049l"

// ResetTerminalAfterRunError writes the modes-off reset after Bubble Tea's
// Run returned an error (a panic or a kill). Bubble Tea has already left the
// alternate screen and printed the panic on the main screen, so the reset
// does not leave the alternate screen: that would restore the saved cursor
// and the next output would overwrite the panic header and trace.
func ResetTerminalAfterRunError(w io.Writer) {
	_, _ = io.WriteString(w, terminalModesOffSequence)
}

// ResetTerminalOnSignal writes the full reset from a signal handler that
// ends in os.Exit while the TUI runs: modes off, then leave the alternate
// screen, which the TUI still holds. Without it a killed TUI leaves mouse
// reporting on and the shell prompt fills with mouse escape codes.
func ResetTerminalOnSignal(w io.Writer) {
	_, _ = io.WriteString(w, terminalModesOffSequence+leaveAltScreenSequence)
}
