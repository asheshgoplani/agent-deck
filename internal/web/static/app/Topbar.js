// Topbar.js -- Full-width topbar with sidebar toggle, brand, connection, theme, profile, info drawer toggle
import { html } from 'htm/preact'
import { ThemeToggle } from './ThemeToggle.js'
import { ProfileDropdown } from './ProfileDropdown.js'
import { ConnectionIndicator } from './ConnectionIndicator.js'
import { activeTabSignal, infoDrawerOpenSignal } from './state.js'
import { PushControls } from './PushControls.js'

export function Topbar({ onToggleSidebar, sidebarOpen }) {
  return html`
    <header class="flex items-center justify-between px-sp-12 py-sp-8
      dark:bg-tn-panel bg-white border-b dark:border-tn-muted/20 border-gray-200
      flex-shrink-0 relative z-50">
      <div class="flex items-center gap-3">
        <button
          type="button"
          onClick=${onToggleSidebar}
          class="lg:hidden text-lg dark:text-tn-muted text-gray-500 hover:dark:text-tn-fg hover:text-gray-700 transition-colors p-1"
          aria-label=${sidebarOpen ? 'Close sidebar' : 'Open sidebar'}
          aria-expanded=${sidebarOpen}
        >
          ${sidebarOpen ? '\u2715' : '\u2630'}
        </button>
        <span class="font-semibold text-sm dark:text-tn-fg text-gray-900">Agent Deck</span>
      </div>
      <div class="flex items-center gap-3">
        <button
          type="button"
          onClick=${() => { activeTabSignal.value = activeTabSignal.value === 'costs' ? 'terminal' : 'costs' }}
          class="text-xs dark:text-tn-muted text-gray-500 hover:dark:text-tn-fg hover:text-gray-700 transition-colors px-2 py-1 rounded hover:dark:bg-tn-muted/10 hover:bg-gray-100"
          aria-label=${activeTabSignal.value === 'costs' ? 'Switch to terminal' : 'Open cost dashboard'}
          title="Cost Dashboard"
        >
          ${activeTabSignal.value === 'costs' ? 'Terminal' : 'Costs'}
        </button>
        <${ConnectionIndicator} />
        <${ThemeToggle} />
        <${ProfileDropdown} />
        <button
          type="button"
          onClick=${() => { infoDrawerOpenSignal.value = !infoDrawerOpenSignal.value }}
          class="text-xs dark:text-tn-muted text-gray-500 hover:dark:text-tn-fg hover:text-gray-700 transition-colors px-2 py-1 rounded hover:dark:bg-tn-muted/10 hover:bg-gray-100"
          title="Toggle info panel"
          aria-expanded=${infoDrawerOpenSignal.value}
          aria-label=${infoDrawerOpenSignal.value ? 'Close info panel' : 'Open info panel'}
        >
          Info
        </button>
        <${PushControls} />
      </div>
    </header>
  `
}
