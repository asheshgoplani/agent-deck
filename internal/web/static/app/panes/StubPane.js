// panes/StubPane.js -- Informative placeholder for tabs whose data lives in
// the TUI but is not exposed via the web API today.
//
// Used by MCP / Skills / Conductor / Watchers tabs. The bundle's design
// expects rich data here (toggles, graphs, event streams). Per the parity
// matrix those are MISSING endpoints, so this pane explains the gap rather
// than inventing data.
import { html } from 'htm/preact'

export function StubPane({ title, message, hotkey }) {
  return html`
    <div class="costs">
      <div class="chart-card" style="text-align: center; padding: 32px;">
        <div class="title" style="font-size: 14px;">${title}</div>
        <div style="font-family: var(--mono); font-size: 12px; color: var(--text-dim); line-height: 1.6; margin-top: 12px;">
          ${message}
          ${hotkey && html`<div style="margin-top: 12px;">In the TUI: press <span class="kbd" style="border:1px solid var(--border); padding: 1px 6px; border-radius: 3px; color: var(--text);">${hotkey}</span></div>`}
        </div>
      </div>
    </div>
  `
}
