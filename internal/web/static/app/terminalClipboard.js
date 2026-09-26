// terminalClipboard.js -- OSC 52 clipboard writes for the web terminal (#2370).
//
// Programs in a pane copy text by printing OSC 52: ESC ] 52 ; <targets> ;
// <base64 payload> BEL. tmux forwards it (set-clipboard on, and the bridge's
// TERM=xterm-256color advertises the clipboard feature), and Claude Code's own
// fullscreen selection emits it too. xterm.js parses the sequence but has no
// default handler, so without this the copy never left the pane, and only the
// host machine's own pasteboard (via tmux's copy command) ever saw it.
//
// Writes only. A payload of "?" asks the terminal to report the clipboard back
// into the pane; answering that would let anything running in a session read
// the viewer's clipboard, so queries are consumed and never answered. An empty
// or undecodable payload is ignored rather than clearing the clipboard.
//
// No xterm import here: the terminal and the clipboard are passed in, which
// keeps this module unit-testable on its own.

export const OSC52 = 52

// Upper bound on the base64 payload, so a runaway or hostile program cannot
// push an arbitrarily large string into the viewer's clipboard.
export const OSC52_MAX_PAYLOAD = 1024 * 1024

// decodeOsc52 returns the text an OSC 52 write carries, or null when the
// sequence is a query, empty, too large, or not valid base64 UTF-8. data is
// everything after "52;", i.e. "<targets>;<payload>".
export function decodeOsc52(data) {
  const sep = data.indexOf(';')
  if (sep < 0) return null
  const payload = data.slice(sep + 1)
  if (!payload || payload === '?' || payload.length > OSC52_MAX_PAYLOAD) return null
  try {
    const binary = atob(payload)
    const bytes = Uint8Array.from(binary, (ch) => ch.charCodeAt(0))
    return new TextDecoder('utf-8', { fatal: true }).decode(bytes)
  } catch (_e) {
    return null
  }
}

// registerOsc52Handler installs the handler on terminal and returns xterm's
// disposable. clipboard defaults to navigator.clipboard, which only exists on
// secure origins (HTTPS or localhost); without it the sequence is dropped. A
// rejected write (no focus, browser policy) is swallowed so it never
// interrupts the terminal.
export function registerOsc52Handler(terminal, clipboard = globalThis.navigator?.clipboard) {
  return terminal.parser.registerOscHandler(OSC52, (data) => {
    const text = decodeOsc52(data)
    if (text !== null && clipboard?.writeText) {
      clipboard.writeText(text).catch(() => {})
    }
    return true
  })
}
