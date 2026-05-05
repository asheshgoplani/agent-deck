// ConfirmDialog.js -- Generic confirmation modal (used for delete confirmation).
// Restyled (PR-B) to use the bundle's `.dialog` / `.dh` / `.db` / `.df` /
// `.btn` classes from app.css. Functional contract preserved: focus moves to
// Cancel on mount (fix #784); Enter activates the focused button; Esc closes.
import { html } from 'htm/preact'
import { useEffect, useRef } from 'preact/hooks'
import { Icon, ICONS } from './icons.js'
import { confirmDialogSignal } from './state.js'

export function ConfirmDialog({ message, onConfirm }) {
  const cancelRef = useRef(null)

  useEffect(() => {
    if (cancelRef.current) cancelRef.current.focus()
  }, [])

  const close = () => (confirmDialogSignal.value = null)
  const confirm = () => {
    onConfirm()
    confirmDialogSignal.value = null
  }
  const onKeyDown = (e) => { if (e.key === 'Escape') { e.stopPropagation(); close() } }

  return html`
    <div class="overlay" onClick=${(e) => e.target === e.currentTarget && close()}>
      <div role="dialog" aria-modal="true" aria-label="Confirm action"
           class="dialog" style="max-width: 460px;"
           onClick=${e => e.stopPropagation()}
           onKeyDown=${onKeyDown}>
        <div class="dh">
          <span class="kicker" style="color: var(--tn-red); background: rgba(247,118,142,0.12);">CONFIRM</span>
          <div class="t">Are you sure?</div>
          <button type="button" class="icon-btn" onClick=${close} aria-label="Close">
            <${Icon} d=${ICONS.x}/>
          </button>
        </div>
        <div class="db">
          <div style="font-family: var(--sans); color: var(--text); line-height: 1.55;">${message}</div>
        </div>
        <div class="df">
          <button type="button" class="btn ghost" ref=${cancelRef} onClick=${close}>Cancel</button>
          <button type="button" class="btn danger" onClick=${confirm}>Delete</button>
        </div>
      </div>
    </div>
  `
}
