// Toast.js -- Global toast notifications, restyled (PR-B) to use the bundle's
// `.toast` class with role-styled border tints (errors bordered in red,
// success in green, info in accent blue).
//
// Behavior contract preserved verbatim from pre-redesign:
//   - Visible stack capped at 3.
//   - Eviction: oldest non-error first; only evict errors when all visible
//     are errors AND a new error arrives.
//   - info / success auto-dismiss after 5s.
//   - error toasts require explicit click.
//   - Dismissed toasts pushed to toastHistorySignal (cap 50, localStorage
//     key `agentdeck_toast_history`).
//   - aria-live="assertive" for errors, "polite" otherwise.
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
  } catch (_) { /* incognito */ }
}

export function addToast(message, type) {
  const resolvedType = type || 'error'
  const newToast = { id: ++nextId, message, type: resolvedType, createdAt: Date.now() }
  let next = [...toastsSignal.value, newToast]
  if (next.length > 3) {
    const nonErrorIdx = next.findIndex(t => t.type !== 'error')
    if (nonErrorIdx >= 0) {
      const [evicted] = next.splice(nonErrorIdx, 1)
      pushToHistory(evicted)
    } else {
      const evicted = next.shift()
      pushToHistory(evicted)
    }
  }
  toastsSignal.value = next
  if (newToast.type !== 'error') {
    setTimeout(() => removeToast(newToast.id), AUTO_DISMISS_MS)
  }
}

export function removeToast(id) {
  const removed = toastsSignal.value.find(t => t.id === id)
  if (removed) pushToHistory(removed)
  toastsSignal.value = toastsSignal.value.filter(t => t.id !== id)
}

function ToastItem({ id, message, type }) {
  const borderColor =
    type === 'error'   ? 'var(--tn-red)'
    : type === 'info'  ? 'var(--accent)'
    : 'var(--tn-green)'
  const sigil =
    type === 'error'   ? '✕'
    : type === 'info'  ? 'ℹ'
    : '✓'
  return html`
    <div class="toast" data-testid="toast" style=${{ borderColor, position: 'relative', pointerEvents: 'auto' }}>
      <span class="t" style=${{ color: borderColor }}>${sigil}</span>
      <span style="margin-left: 6px;">${message}</span>
      <button type="button"
        onClick=${() => removeToast(id)}
        aria-label="Dismiss"
        data-testid="toast-dismiss"
        style="background: transparent; border: 0; color: var(--muted); cursor: pointer;
               margin-left: 10px; padding: 0 4px; font-size: 12px;">✕</button>
    </div>
  `
}

export function ToastContainer() {
  const toasts = toastsSignal.value
  if (toasts.length === 0) return null
  const errors = toasts.filter(t => t.type === 'error')
  const nonErrors = toasts.filter(t => t.type !== 'error')
  // Stack toasts vertically above the footer; the bundle's `.toast` class
  // anchors a single instance to the bottom-right, so for multiple we
  // wrap with absolute-positioned stack.
  return html`
    <div style=${{
      position: 'fixed', bottom: '40px', right: '14px', zIndex: 70,
      display: 'flex', flexDirection: 'column', gap: '6px',
      pointerEvents: 'none', maxWidth: '420px',
    }}>
      ${errors.length > 0 && html`
        <div role="alert" aria-live="assertive" style=${{ display: 'flex', flexDirection: 'column', gap: '6px' }}>
          ${errors.map(t => html`<${ToastItem} key=${t.id} ...${t}/>`)}
        </div>
      `}
      ${nonErrors.length > 0 && html`
        <div role="status" aria-live="polite" style=${{ display: 'flex', flexDirection: 'column', gap: '6px' }}>
          ${nonErrors.map(t => html`<${ToastItem} key=${t.id} ...${t}/>`)}
        </div>
      `}
    </div>
  `
}
