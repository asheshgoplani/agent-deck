// terminalPaste.js -- clipboard bridge for the web terminal.
//
// xterm 6.0's own paste path can fail and destroy the system clipboard on
// WSL2+Chrome when focus is not on .xterm-helper-textarea, so TerminalPanel
// captures the paste event first and reads clipboardData itself. What it must
// NOT do is take over the ENCODING as well: the text goes back to
// terminal.paste(), which is xterm's public paste entry point.
//
// That encoding is the whole fix. terminal.paste() rewrites line breaks to CR
// and -- when the pane's app has enabled DECSET 2004 -- wraps the payload in
// the bracketed-paste markers ESC[200~ ... ESC[201~. Those markers are what
// tell an agent TUI "this is a paste, insert the newlines" instead of "these
// are keystrokes". Writing the raw clipboard text to the pane instead made
// Claude Code drop every line break, landing a multi-line paste as one run-on
// line, while the same paste through a native terminal kept its lines. (tmux
// is bracketed-paste aware in both directions: it forwards the markers to
// panes whose app enabled 2004 and strips them for panes that did not, so a
// bare shell prompt still behaves.)
//
// Routing through terminal.paste() also puts paste back on the pane's single
// guarded write path -- it emits through onData, the same as typing, so
// TerminalPanel's sendInput() applies the read-only/socket checks once for
// every producer. No xterm import here: the terminal is passed in, which keeps
// this module unit-testable on its own.

// createPasteHandler returns a `paste` event listener for the terminal
// container. It is installed in the capture phase, so stopping the event also
// stops xterm's own listener from pasting the same text a second time.
export function createPasteHandler(terminal) {
  return (event) => {
    const text = event.clipboardData?.getData('text/plain')
    if (!text) return
    event.preventDefault()
    event.stopPropagation()
    terminal.paste(text)
  }
}
