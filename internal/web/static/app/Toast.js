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
import { toastsSignal, removeToast } from './toasts.js'

// addToast/removeToast moved to toasts.js (leaf module) so api.js can call
// them without importing this component; re-exported for existing callers.
export { addToast, removeToast } from './toasts.js'

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
