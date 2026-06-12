// ToastHistoryDrawer.js -- Historical dismissed toasts.
// Restyled (PR-B) to use the bundle's overlay + dialog patterns.
import { html } from 'htm/preact'
import { Icon, ICONS } from './icons.js'
import { toastHistorySignal, toastHistoryOpenSignal } from './state.js'

function formatTime(ms) {
  if (!ms) return ''
  try {
    return new Date(ms).toLocaleTimeString([], { hour: '2-digit', minute: '2-digit', second: '2-digit' })
  } catch (_) {
    return ''
  }
}

export function ToastHistoryDrawerToggle() {
  const count = toastHistorySignal.value.length
  return html`
    <button type="button"
      class=${`icon-btn ${toastHistoryOpenSignal.value ? 'active' : ''}`}
      onClick=${() => { toastHistoryOpenSignal.value = !toastHistoryOpenSignal.value }}
      aria-label=${'Toast history (' + count + ' entries)'}
      aria-expanded=${toastHistoryOpenSignal.value}
      title="Toast history"
      data-testid="toast-history-toggle"
      style="position: relative;">
      <svg width="14" height="14" fill="none" stroke="currentColor" stroke-width="2"
           stroke-linecap="round" stroke-linejoin="round" viewBox="0 0 24 24">
        <path d="M12 8v4l3 3"/>
        <circle cx="12" cy="12" r="9"/>
      </svg>
      ${count > 0 && html`<span class="pip" style="background: var(--accent); box-shadow: 0 0 6px var(--accent);"/>`}
    </button>
  `
}

export function ToastHistoryDrawer() {
  if (!toastHistoryOpenSignal.value) return null
  const history = toastHistorySignal.value
  const close = () => { toastHistoryOpenSignal.value = false }
  return html`
    <div class="overlay" role="dialog" aria-modal="true" aria-label="Toast history"
         data-testid="toast-history-drawer"
         style="justify-content: flex-end; padding: 0;"
         onClick=${(e) => { if (e.target === e.currentTarget) close() }}>
      <div class="dialog" style="width: 420px; max-width: 100vw; height: 100vh; max-height: 100vh; border-radius: 0; border-right: 0;"
           onClick=${e => e.stopPropagation()}>
        <div class="dh">
          <span class="kicker">HISTORY</span>
          <div class="t">Toast history</div>
          <button type="button" class="icon-btn" onClick=${close} aria-label="Close toast history">
            <${Icon} d=${ICONS.x}/>
          </button>
        </div>
        <div class="db" style="padding: 0;">
          ${history.length === 0 && html`
            <div style="padding: 20px; font-family: var(--mono); font-size: 12px; color: var(--muted); text-align: center;">
              No dismissed toasts yet.
            </div>
          `}
          ${history.slice().reverse().map(t => html`
            <div key=${t.id}
                 data-testid="toast-history-entry"
                 style=${{
                   padding: '10px 14px',
                   borderBottom: '1px solid var(--border)',
                   background: t.type === 'error' ? 'rgba(247,118,142,0.06)' : 'transparent',
                 }}>
              <div style=${{
                fontFamily: 'var(--mono)', fontSize: '10px',
                color: t.type === 'error' ? 'var(--tn-red)' : 'var(--muted)',
                letterSpacing: '0.06em',
                marginBottom: '4px',
              }}>
                ${formatTime(t.createdAt)} · ${t.type}
              </div>
              <div style=${{
                fontSize: '12.5px',
                color: t.type === 'error' ? 'var(--tn-red)' : 'var(--text)',
              }}>${t.message}</div>
            </div>
          `)}
        </div>
      </div>
    </div>
  `
}
