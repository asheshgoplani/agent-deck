// Toast.js -- Global toast notifications (WEB-P0-4 + POL-7)
//
// Contract (06-CONTEXT.md lines 84-96):
//   - Visible stack capped at 3.
//   - When a 4th toast arrives, evict oldest non-error first (FIFO).
//     Errors are evicted only if all 3 visible are errors AND a new error arrives.
//   - info / success toasts auto-dismiss after 5 seconds.
//   - error toasts do NOT auto-dismiss (require explicit click).
//   - Dismissed toasts push into toastHistorySignal (capped at 50,
//     localStorage-persisted under key `agentdeck_toast_history`).
//   - Errors use aria-live="assertive"; info / success use aria-live="polite".
import { html } from 'htm/preact'
import { toastsSignal, toastHistorySignal } from './state.js'

let nextId = 0
const HISTORY_CAP = 50
const AUTO_DISMISS_MS = 5000
const LOCAL_STORAGE_KEY = 'agentdeck_toast_history'

function pushToHistory(toast) {
  if (!toast) return
  const next = [...toastHistorySignal.value, toast].slice(-HISTORY_CAP)
  toastHistorySignal.value = next
  try {
    localStorage.setItem(LOCAL_STORAGE_KEY, JSON.stringify(next))
  } catch (_) {
    // localStorage may throw in incognito / privacy modes; drop persistence silently.
  }
}

export function addToast(message, type) {
  const resolvedType = type || 'error'
  const newToast = { id: ++nextId, message, type: resolvedType, createdAt: Date.now() }
  let next = [...toastsSignal.value, newToast]
  // Cap visible stack at 3 (literal per 06-CONTEXT.md line 91)
  if (next.length > 3) {
    // Evict oldest non-error first; only evict errors if all visible are errors.
    const nonErrorIdx = next.findIndex(t => t.type !== 'error')
    if (nonErrorIdx >= 0) {
      const [evicted] = next.splice(nonErrorIdx, 1)
      pushToHistory(evicted)
    } else {
      const evicted = next.shift() // FIFO error eviction (only when all visible are errors)
      pushToHistory(evicted)
    }
  }
  toastsSignal.value = next
  // Auto-dismiss info / success only. Errors require explicit X click.
  if (newToast.type !== 'error') {
    setTimeout(() => removeToast(newToast.id), AUTO_DISMISS_MS)
  }
}

export function removeToast(id) {
  const removed = toastsSignal.value.find(t => t.id === id)
  if (removed) pushToHistory(removed)
  toastsSignal.value = toastsSignal.value.filter(t => t.id !== id)
}

export function ToastContainer() {
  const toasts = toastsSignal.value
  const errors = toasts.filter(t => t.type === 'error')
  const nonErrors = toasts.filter(t => t.type !== 'error')
  if (toasts.length === 0) return null

  return html`
    <div class="fixed bottom-4 right-4 z-toast flex flex-col gap-2 max-w-sm pointer-events-none">
      ${errors.length > 0 && html`
        <div role="alert" aria-live="assertive" class="flex flex-col gap-2 pointer-events-auto">
          ${errors.map(toast => html`<${ToastItem} key=${toast.id} ...${toast} />`)}
        </div>
      `}
      ${nonErrors.length > 0 && html`
        <div role="status" aria-live="polite" class="flex flex-col gap-2 pointer-events-auto">
          ${nonErrors.map(toast => html`<${ToastItem} key=${toast.id} ...${toast} />`)}
        </div>
      `}
    </div>
  `
}

function ToastItem({ id, message, type }) {
  const bgColor = type === 'error'
    ? 'dark:bg-tn-red/20 bg-red-50 dark:border-tn-red/40 border-red-200'
    : 'dark:bg-tn-green/20 bg-green-50 dark:border-tn-green/40 border-green-200'
  const textColor = type === 'error'
    ? 'dark:text-tn-red text-red-700'
    : 'dark:text-tn-green text-green-700'
  const iconColor = type === 'error'
    ? 'dark:text-tn-red text-red-500'
    : 'dark:text-tn-green text-green-500'

  return html`
    <div class="flex items-start gap-2 px-3 py-2.5 rounded-lg border shadow-lg
                ${bgColor} animate-slide-in-right">
      <svg class="w-4 h-4 mt-0.5 flex-shrink-0 ${iconColor}" fill="none" stroke="currentColor" viewBox="0 0 24 24" aria-hidden="true">
        ${type === 'error'
          ? html`<path stroke-linecap="round" stroke-linejoin="round" stroke-width="2"
                       d="M12 8v4m0 4h.01M21 12a9 9 0 11-18 0 9 9 0 0118 0z"/>`
          : html`<path stroke-linecap="round" stroke-linejoin="round" stroke-width="2"
                       d="M9 12l2 2 4-4m6 2a9 9 0 11-18 0 9 9 0 0118 0z"/>`
        }
      </svg>
      <span class="text-sm flex-1 ${textColor}">${message}</span>
      <button
        type="button"
        onClick=${() => removeToast(id)}
        class="flex-shrink-0 min-w-[44px] min-h-[44px] flex items-center justify-center ${textColor} opacity-60 hover:opacity-100 transition-opacity"
        aria-label="Dismiss"
      >\u2715</button>
    </div>
  `
}
