// terminalKeys.js -- pure keyboard-decision helper for the web terminal.
//
// Dependency-free (no xterm/preact imports) so it is unit-testable in isolation
// and importable by TerminalPanel.js's custom key handler.

// terminalKeymap maps a browser KeyboardEvent to the byte sequence a native
// terminal would send for that gesture, or null to let xterm handle the key as
// usual. The caller writes the returned bytes to the pane and suppresses
// xterm's own output for that key.
//
// It exists because xterm-in-a-browser loses keystrokes a real terminal
// delivers: it emits a bare `\r` for Shift+Enter (submits instead of newline),
// nothing at all for Cmd+Arrow, and shell-incompatible CSI sequences for
// Option+Arrow. Every sequence below was verified to do the intended thing in
// BOTH readline shells (zsh/bash) AND agent TUIs (Claude, Codex) with no
// terminal extended-keys negotiation:
//
//   Shift+Enter   -> \n       (0x0a / Ctrl+J)  insert newline, don't submit
//   Cmd+Left      -> \x01     (Ctrl+A)         cursor to line start
//   Cmd+Right     -> \x05     (Ctrl+E)         cursor to line end
//   Cmd+Backspace -> \x15     (Ctrl+U)         delete to line start
//   Option+Left   -> \x1bb    (ESC b)          cursor back one word
//   Option+Right  -> \x1bf    (ESC f)          cursor forward one word
//
// Shift+Enter is platform-independent; the Cmd/Option line-editing conventions
// are macOS-only and gated behind isMac so they don't hijack the Super/Alt keys
// on Linux/Windows. Option+Backspace is intentionally NOT mapped: xterm already
// emits `ESC DEL` for it, which is the correct word-delete on every target.
//
// Only lone modifier combinations match (e.g. Cmd without Ctrl/Alt/Shift); any
// extra modifier, a keyup, or an in-progress IME composition returns null so
// the key falls through to xterm untouched.
export function terminalKeymap(e, isMac) {
  if (e.type !== 'keydown' || e.isComposing) return null

  // Shift+Enter -> newline. All platforms.
  if (e.key === 'Enter' && e.shiftKey && !e.ctrlKey && !e.altKey && !e.metaKey) {
    return '\n'
  }

  if (!isMac) return null

  const cmd = e.metaKey && !e.ctrlKey && !e.altKey && !e.shiftKey
  const opt = e.altKey && !e.ctrlKey && !e.metaKey && !e.shiftKey

  if (cmd && e.key === 'ArrowLeft') return '\x01'   // Ctrl+A: line start
  if (cmd && e.key === 'ArrowRight') return '\x05'  // Ctrl+E: line end
  if (cmd && e.key === 'Backspace') return '\x15'   // Ctrl+U: delete to line start
  if (opt && e.key === 'ArrowLeft') return '\x1bb'  // ESC b: word back
  if (opt && e.key === 'ArrowRight') return '\x1bf' // ESC f: word forward

  return null
}
