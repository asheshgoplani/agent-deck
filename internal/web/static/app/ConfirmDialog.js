// ConfirmDialog.js -- Generic confirmation modal.
// Restyled (PR-B) to use the bundle's `.dialog` / `.dh` / `.db` / `.df` /
// `.btn` classes from app.css. Functional contract preserved: focus moves to
// Cancel on mount (fix #784); Enter activates the focused button; Esc closes.
//
// Tone/label are part of the payload because this modal is shared by actions
// of different severity. It was written delete-only (#519) and later reused
// for archive, close and worktree-finish, so every caller rendered a red
// "Delete" button — archiving offered to "Delete" the session. The TUI is the
// reference (internal/ui/confirm_dialog.go): archive and close are ColorYellow
// with "Archive"/"Close" buttons, delete is ColorRed with "Delete".
// Defaults stay delete-shaped so a caller that passes neither is unchanged.
import { html } from 'htm/preact'
import { useEffect, useRef } from 'preact/hooks'
import { Icon, ICONS } from './icons.js'
import { confirmDialogSignal } from './state.js'

// Presentation per tone. Kicker tones are classes, not `style=` attributes, so
// app.css owns the colors; a style attribute would beat any stylesheet rule.
// (Comment prose here is scanned by Tailwind — styles.src.css aims it at
// app/**/*.js — so a bare utility name in a sentence emits a stray rule and
// drifts `make css-verify`. Hyphenate or quote such words.)
export const CONFIRM_TONES = {
  danger: { btnClass: 'btn danger', kickerClass: 'kicker danger' },
  warn:   { btnClass: 'btn warn',   kickerClass: 'kicker warn' },
}

const DEFAULT_TONE = 'danger'

// "Dismiss" rather than the house-standard "Close" used by the other dialogs:
// the close-session confirm button is itself labeled "Close", and two buttons
// sharing an accessible name is ambiguous for screen readers (and for
// getByRole locators in the e2e suite).
const DISMISS_LABEL = 'Dismiss'

// resolveConfirm maps a confirmDialogSignal payload onto what the modal
// renders. Unknown tones fall back to danger: a typo should degrade to the
// most cautious styling, never to an unstyled button.
export function resolveConfirm({ tone, confirmLabel, title } = {}) {
  const t = CONFIRM_TONES[tone] || CONFIRM_TONES[DEFAULT_TONE]
  return {
    btnClass: t.btnClass,
    kickerClass: t.kickerClass,
    confirmLabel: confirmLabel || 'Delete',
    title: title || 'Are you sure?',
  }
}

export function ConfirmDialog({ message, onConfirm, tone, confirmLabel, title }) {
  const cancelRef = useRef(null)
  const { btnClass, kickerClass, confirmLabel: label, title: heading } =
    resolveConfirm({ tone, confirmLabel, title })

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
          <span class=${kickerClass}>CONFIRM</span>
          <div class="t">${heading}</div>
          <button type="button" class="icon-btn" onClick=${close} aria-label=${DISMISS_LABEL}>
            <${Icon} d=${ICONS.x}/>
          </button>
        </div>
        <div class="db">
          <div style="font-family: var(--sans); color: var(--text); line-height: 1.55;">${message}</div>
        </div>
        <div class="df">
          <button type="button" class="btn ghost" ref=${cancelRef} onClick=${close}>Cancel</button>
          <button type="button" class=${btnClass} onClick=${confirm}>${label}</button>
        </div>
      </div>
    </div>
  `
}
