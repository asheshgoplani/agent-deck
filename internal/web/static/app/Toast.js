// Toast.js -- Global error toast notifications
import { html } from 'htm/preact'
import { toastsSignal } from './state.js'

let nextId = 0

export function addToast(message, type) {
  const id = ++nextId
  toastsSignal.value = [...toastsSignal.value, { id, message, type: type || 'error' }]
  setTimeout(() => removeToast(id), 5000)
}

function removeToast(id) {
  toastsSignal.value = toastsSignal.value.filter(t => t.id !== id)
}

export function ToastContainer() {
  const toasts = toastsSignal.value
  if (toasts.length === 0) return null

  return html`
    <div class="fixed bottom-4 right-4 z-[100] flex flex-col gap-2 max-w-sm" role="alert" aria-live="polite">
      ${toasts.map(toast => html`
        <${ToastItem} key=${toast.id} ...${toast} />
      `)}
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
      <svg class="w-4 h-4 mt-0.5 flex-shrink-0 ${iconColor}" fill="none" stroke="currentColor" viewBox="0 0 24 24">
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
        class="flex-shrink-0 ${textColor} opacity-60 hover:opacity-100 transition-opacity"
        aria-label="Dismiss"
      >\u2715</button>
    </div>
  `
}
