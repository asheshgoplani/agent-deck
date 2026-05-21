// KeyboardShortcuts.js -- Help overlay listing all keybindings, opened with `?`.
//
// Implements feat(web): keyboard parity with TUI for top-10 bindings (#780).
// Combines Option 1 (discoverable overlay) + Option 2 (direct key mapping):
// the overlay is the docs surface for Web-only users who don't have the TUI
// in front of them.
import { html } from 'htm/preact'
import { Icon, ICONS } from './icons.js'
import { shortcutsOverlaySignal } from './state.js'

const BINDINGS = [
  { keys: ['/'],               label: 'Focus session filter / search' },
  { keys: ['j'],               label: 'Move focus down (next session)' },
  { keys: ['k'],               label: 'Move focus up (previous session)' },
  { keys: ['Enter'],           label: 'Open focused session' },
  { keys: ['Shift', 'Enter'],  label: 'Open focused session in new browser tab' },
  { keys: ['n'],               label: 'New session dialog' },
  { keys: ['r'],               label: 'Rename focused session (TUI-only today)' },
  { keys: ['Shift', 'D'],      label: 'Close focused session (stop process, keep metadata)' },
  { keys: ['Ctrl', 'Z'],       label: 'Undo last delete (within 30s)' },
  { keys: ['q'],               label: 'Close current modal / overlay' },
  { keys: ['Esc'],             label: 'Close modal / unfocus input' },
  { keys: ['?'],               label: 'Toggle this help overlay' },
  { keys: ['Ctrl', 'K'],       label: 'Command palette' },
  { keys: [']'],               label: 'Toggle right rail' },
]

function Kbd({ k }) {
  return html`<span class="kbd kshort-kbd">${k}</span>`
}

export function KeyboardShortcuts() {
  if (!shortcutsOverlaySignal.value) return null
  const close = () => (shortcutsOverlaySignal.value = false)
  return html`
    <div class="overlay kshort-overlay" role="dialog" aria-label="Keyboard shortcuts"
         data-testid="shortcuts-overlay"
         onClick=${close}>
      <div class="dialog kshort-dialog" onClick=${e => e.stopPropagation()}>
        <div class="dh">
          <span class="kicker">HELP</span>
          <div class="t">Keyboard shortcuts</div>
          <button class="icon-btn" onClick=${close} aria-label="Close help">
            <${Icon} d=${ICONS.x}/>
          </button>
        </div>
        <div class="db">
          <table class="kshort-table">
            <tbody>
              ${BINDINGS.map(b => html`
                <tr key=${b.keys.join('+')}>
                  <td class="kshort-keys">
                    ${b.keys.map((k, i) => html`
                      ${i > 0 && html`<span class="kshort-plus">+</span>`}
                      <${Kbd} k=${k}/>
                    `)}
                  </td>
                  <td class="kshort-label">${b.label}</td>
                </tr>
              `)}
            </tbody>
          </table>
          <div class="kshort-foot">
            Web binds the most-used TUI keys (issue #780). Web-only actions
            (e.g. <span class="kbd">Ctrl</span>+<span class="kbd">K</span>) are
            included for completeness.
          </div>
        </div>
      </div>
    </div>
  `
}
