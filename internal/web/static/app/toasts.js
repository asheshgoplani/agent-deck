// toasts.js -- Toast state and the addToast/removeToast helpers.
//
// Split out of state.js and Toast.js so api.js can raise a toast without
// importing Toast.js, which imports state.js, which imports api.js. That
// three-module cycle (api.js -> Toast.js -> state.js -> api.js) is gone:
// this module depends only on @preact/signals. state.js re-exports the two
// signals and Toast.js re-exports the helpers, so existing imports keep
// working unchanged.
//
// Behavior contract preserved verbatim from Toast.js:
//   - Visible stack capped at 3.
//   - Eviction: oldest non-error first; only evict errors when all visible
//     are errors AND a new error arrives.
//   - info / success auto-dismiss after 5s.
//   - error toasts require explicit click.
//   - Dismissed toasts pushed to toastHistorySignal (cap 50, localStorage
//     key `agentdeck_toast_history`).
import { signal } from '@preact/signals'

let nextId = 0
const HISTORY_CAP = 50
const AUTO_DISMISS_MS = 5000
const LOCAL_STORAGE_KEY = 'agentdeck_toast_history'

// Global error toasts (Issue F)
export const toastsSignal = signal([])

// Toast history (WEB-P0-4 + POL-7): capped at 50 dismissed toasts.
// Persisted to localStorage key `agentdeck_toast_history`.
// Schema is localStorage-only per milestone rule: NO SQLite schema changes.
function initialToastHistory() {
  try {
    const stored = localStorage.getItem(LOCAL_STORAGE_KEY)
    if (stored) {
      const parsed = JSON.parse(stored)
      if (Array.isArray(parsed)) return parsed.slice(-HISTORY_CAP)
    }
  } catch (_) {
    // localStorage may throw in incognito/privacy modes; start empty.
  }
  return []
}
export const toastHistorySignal = signal(initialToastHistory())

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
