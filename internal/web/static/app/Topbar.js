// Topbar.js -- Full-width topbar with sidebar toggle, brand, connection, theme, profile, info drawer toggle
import { html } from 'htm/preact'
import { ThemeToggle } from './ThemeToggle.js'
import { ProfileDropdown } from './ProfileDropdown.js'
import { ConnectionIndicator } from './ConnectionIndicator.js'
import { activeTabSignal, infoDrawerOpenSignal } from './state.js'
import { PushControls } from './PushControls.js'
import { ToastHistoryDrawerToggle } from './ToastHistoryDrawer.js'

export function Topbar({ onToggleSidebar, sidebarOpen }) {
  return html`
    <header class="flex items-center justify-between px-sp-12 py-sp-8
      dark:bg-tn-panel bg-white border-b dark:border-tn-muted/20 border-gray-200
      flex-shrink-0 relative z-sticky">
      <div class="flex items-center gap-3 min-w-0">
        <button
          type="button"
          onClick=${onToggleSidebar}
          class="lg:hidden relative z-topbar-primary pointer-events-auto p-2 -ml-2 min-w-[44px] min-h-[44px] flex items-center justify-center rounded dark:text-tn-muted text-gray-500 hover:dark:text-tn-fg hover:text-gray-700 hover:dark:bg-tn-muted/10 hover:bg-gray-100 transition-colors"
          aria-label=${sidebarOpen ? 'Close sidebar' : 'Open sidebar'}
          aria-expanded=${sidebarOpen}
        >
          ${sidebarOpen
            ? html`<svg class="w-6 h-6" fill="none" stroke="currentColor" viewBox="0 0 24 24">
                <path stroke-linecap="round" stroke-linejoin="round" stroke-width="2" d="M6 18L18 6M6 6l12 12"/>
              </svg>`
            : html`<svg class="w-6 h-6" fill="none" stroke="currentColor" viewBox="0 0 24 24">
                <path stroke-linecap="round" stroke-linejoin="round" stroke-width="2" d="M4 6h16M4 12h16M4 18h16"/>
              </svg>`
          }
        </button>
        <span class="flex items-center gap-1.5">
          <svg class="w-5 h-5" viewBox="0 0 64 64" fill="none" aria-hidden="true">
            <rect x="18" y="8" width="36" height="44" rx="6" fill="currentColor" opacity="0.2"/>
            <rect x="13" y="12" width="36" height="44" rx="6" fill="currentColor" opacity="0.4"/>
            <rect x="8" y="16" width="36" height="44" rx="6" fill="currentColor" opacity="0.7"/>
            <rect x="14" y="28" width="16" height="3" rx="1.5" fill="#73daca"/>
            <circle cx="34" cy="29.5" r="2" fill="#73daca"/>
          </svg>
          <span class="font-semibold text-sm dark:text-tn-fg text-gray-900 md:hidden lg:inline">Agent Deck</span>
        </span>
      </div>
      <div class="relative z-topbar flex items-center gap-3 flex-shrink-0">
        <button
          type="button"
          onClick=${() => { activeTabSignal.value = activeTabSignal.value === 'costs' ? 'terminal' : 'costs' }}
          class="text-xs dark:text-tn-muted text-gray-500 hover:dark:text-tn-fg hover:text-gray-700 transition-colors px-3 py-2 min-h-[44px] flex items-center rounded hover:dark:bg-tn-muted/10 hover:bg-gray-100"
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
          class="text-xs dark:text-tn-muted text-gray-500 hover:dark:text-tn-fg hover:text-gray-700 transition-colors px-3 py-2 min-h-[44px] flex items-center rounded hover:dark:bg-tn-muted/10 hover:bg-gray-100"
          title="Toggle info panel"
          aria-expanded=${infoDrawerOpenSignal.value}
          aria-label=${infoDrawerOpenSignal.value ? 'Close info panel' : 'Open info panel'}
        >
          Info
        </button>
        <${ToastHistoryDrawerToggle} />
        <${PushControls} />
      </div>
    </header>
  `
}
