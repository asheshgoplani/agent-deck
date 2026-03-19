// Package terminal provides shared terminal escape sequence helpers
// used by both the TUI (ui) and PTY attach (tmux) packages.
package terminal

import "io"

// DisableKittyKeyboard writes escape sequences that disable extended keyboard
// protocols so that the terminal reverts to legacy key reporting:
//
//   - Kitty keyboard protocol: ESC[>0u pushes mode 0 (legacy) on the stack.
//   - xterm modifyOtherKeys: ESC[>4;0m disables modifyOtherKeys mode.
//
// Terminals that do not support a protocol ignore the corresponding sequence.
func DisableKittyKeyboard(w io.Writer) {
	_, _ = io.WriteString(w, "\x1b[>0u")   // Disable Kitty protocol
	_, _ = io.WriteString(w, "\x1b[>4;0m") // Disable xterm modifyOtherKeys
}

// RestoreKittyKeyboard writes escape sequences that restore the terminal to
// its previous keyboard mode when the TUI exits.
func RestoreKittyKeyboard(w io.Writer) {
	_, _ = io.WriteString(w, "\x1b[<u")    // Pop Kitty protocol stack
	_, _ = io.WriteString(w, "\x1b[>4;1m") // Restore modifyOtherKeys mode 1 (default)
}
