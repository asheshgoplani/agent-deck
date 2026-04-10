// ToastHistoryDrawer.js -- Historical dismissed toasts drawer (WEB-P0-4 + POL-7)
//
// Shows all toasts that were dismissed or evicted from the visible stack.
// Errors are highlighted. State persists via toastHistorySignal (localStorage
// key `agentdeck_toast_history`, capped at 50 entries by Toast.js).
//
// Two exports:
//   - ToastHistoryDrawerToggle: a small Topbar button (clock-style icon +
//     entry count) that flips toastHistoryOpenSignal.
//   - ToastHistoryDrawer: the drawer dialog itself, gated on
//     toastHistoryOpenSignal so it only renders when open. Mounted by
//     AppShell.js alongside ToastContainer.
import { html } from 'htm/preact'
import { toastHistorySignal, toastHistoryOpenSignal } from './state.js'

function formatTime(ms) {
  if (!ms) return ''
  try {
    const d = new Date(ms)
    return d.toLocaleTimeString([], { hour: '2-digit', minute: '2-digit', second: '2-digit' })
  } catch (_) {
    return ''
  }
}

export function ToastHistoryDrawerToggle() {
  const count = toastHistorySignal.value.length
  return html`
    <button
      type="button"
      onClick=${() => { toastHistoryOpenSignal.value = !toastHistoryOpenSignal.value }}
      class="text-xs dark:text-tn-muted text-gray-500 hover:dark:text-tn-fg hover:text-gray-700
             transition-colors px-3 py-2 min-h-[44px] min-w-[44px] flex items-center gap-1 rounded
             hover:dark:bg-tn-muted/10 hover:bg-gray-100"
      aria-label=${'Toast history (' + count + ' entries)'}
      aria-expanded=${toastHistoryOpenSignal.value}
      title="Toast history"
      data-testid="toast-history-toggle"
    >
      <svg class="w-4 h-4" fill="none" stroke="currentColor" viewBox="0 0 24 24" aria-hidden="true">
        <path stroke-linecap="round" stroke-linejoin="round" stroke-width="2"
              d="M12 8v4l3 3m6-3a9 9 0 11-18 0 9 9 0 0118 0z"/>
      </svg>
      ${count > 0 && html`<span class="text-[10px]">${count}</span>`}
    </button>
  `
}

export function ToastHistoryDrawer() {
  if (!toastHistoryOpenSignal.value) return null
  const history = toastHistorySignal.value
  return html`
    <div
      class="fixed inset-0 z-modal flex items-start justify-end"
      role="dialog"
      aria-modal="true"
      aria-label="Toast history"
      onClick=${(e) => { if (e.target === e.currentTarget) toastHistoryOpenSignal.value = false }}
    >
      <div class="absolute inset-0 bg-black/20"></div>
      <div
        class="relative w-full max-w-md h-full dark:bg-tn-panel bg-white shadow-xl
               border-l dark:border-tn-muted/20 border-gray-200 flex flex-col"
      >
        <header class="flex items-center justify-between px-sp-12 py-sp-8 border-b dark:border-tn-muted/20 border-gray-200">
          <h2 class="text-sm font-semibold dark:text-tn-fg text-gray-900">Toast history</h2>
          <button
            type="button"
            onClick=${() => { toastHistoryOpenSignal.value = false }}
            class="min-w-[44px] min-h-[44px] flex items-center justify-center rounded
                   dark:text-tn-muted hover:dark:text-tn-fg text-gray-500 hover:text-gray-700"
            aria-label="Close toast history"
          >\u2715</button>
        </header>
        <ul class="flex-1 overflow-y-auto divide-y dark:divide-tn-muted/20 divide-gray-200">
          ${history.length === 0 && html`
            <li class="px-sp-12 py-sp-12 text-sm dark:text-tn-muted text-gray-500">No dismissed toasts yet.</li>
          `}
          ${history.slice().reverse().map(t => html`
            <li
              key=${t.id}
              class="px-sp-12 py-sp-8 flex flex-col gap-1
                ${t.type === 'error'
                  ? 'dark:bg-tn-red/10 bg-red-50 dark:text-tn-red text-red-700'
                  : 'dark:text-tn-fg text-gray-700'}"
            >
              <span class="text-xs font-mono dark:text-tn-muted text-gray-600">
                ${formatTime(t.createdAt)} \u00b7 ${t.type}
              </span>
              <span class="text-sm">${t.message}</span>
            </li>
          `)}
        </ul>
      </div>
    </div>
  `
}
