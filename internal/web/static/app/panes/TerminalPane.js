// panes/TerminalPane.js -- Wraps the existing TerminalPanel inside the
// bundle's `.term-wrap` chrome. We DO NOT rewrite TerminalPanel — it owns
// xterm.js + the WebSocket lifecycle and is mature/tested. The wrapper
// only provides outer padding consistent with the new design.
// The pane also offers a read-only Chat view and keeps TerminalPanel mounted while Chat shows.
import { html } from 'htm/preact'
import { useState } from 'preact/hooks'
import { TerminalPanel } from '../TerminalPanel.js'
import { selectedGroupSignal, selectedIdSignal } from '../state.js'
import { activeTabSignal } from '../uiState.js'
import { useLazyComponent } from '../lazyModule.js'

function initialView() {
  try {
    if (localStorage.getItem('agentdeck.sessionView') === 'chat') return 'chat'
  } catch {}
  return 'terminal'
}

export function TerminalPane() {
  const [view, setView] = useState(initialView)
  const sessionId = selectedIdSignal.value
  const chat = view === 'chat' && !!sessionId
  const active = activeTabSignal.value === 'terminal' && !selectedGroupSignal.value
  const Chat = useLazyComponent(chat && active, () => import('../SessionChat.js'), 'sessionChat', m => m.SessionChat)
  function choose(next) {
    setView(next)
    try { localStorage.setItem('agentdeck.sessionView', next) } catch {}
  }
  return html`
    <div class="term-wrap">
      ${sessionId && html`<div class="session-view-toggle" role="group" aria-label="Session view">
        ${['terminal', 'chat'].map(mode => html`
          <button key=${mode} type="button" aria-pressed=${view === mode} onClick=${() => choose(mode)}>
            ${mode === 'terminal' ? 'Terminal' : 'Chat'}
          </button>`)}
      </div>`}
      <div class="session-terminal" hidden=${chat}><${TerminalPanel}/></div>
      ${chat && Chat && html`<${Chat} key=${sessionId} sessionId=${sessionId} active=${active}/>`}
    </div>
  `
}
