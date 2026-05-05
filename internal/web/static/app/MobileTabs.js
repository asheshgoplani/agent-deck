// MobileTabs.js -- Bottom tab bar shown only on phone-class viewports (≤720px).
// CSS-driven visibility via `.mob-tabs` rules in app.css.
import { html } from 'htm/preact'
import { activeTabSignal } from './uiState.js'

const MOBILE_TABS = [
  { id: 'fleet',     label: 'Fleet',    icon: '▦' },
  { id: 'terminal',  label: 'Session',  icon: '›_' },
  { id: 'watchers',  label: 'Watchers', icon: '◇' },
  { id: 'costs',     label: 'Costs',    icon: '$' },
]

export function MobileTabs() {
  const activeTab = activeTabSignal.value
  return html`
    <div class="mob-tabs">
      ${MOBILE_TABS.map(t => html`
        <button key=${t.id}
                class=${`mob-tab ${activeTab === t.id ? 'on' : ''}`}
                onClick=${() => (activeTabSignal.value = t.id)}>
          <span class="mt-ic">${t.icon}</span><span>${t.label}</span>
        </button>
      `)}
    </div>
  `
}
