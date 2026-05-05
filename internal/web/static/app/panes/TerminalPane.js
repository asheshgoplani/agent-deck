// panes/TerminalPane.js -- Wraps the existing TerminalPanel inside the
// bundle's `.term-wrap` chrome. We DO NOT rewrite TerminalPanel — it owns
// xterm.js + the WebSocket lifecycle and is mature/tested. The wrapper
// only provides outer padding consistent with the new design.
import { html } from 'htm/preact'
import { TerminalPanel } from '../TerminalPanel.js'

export function TerminalPane() {
  return html`
    <div class="term-wrap">
      <${TerminalPanel}/>
    </div>
  `
}
