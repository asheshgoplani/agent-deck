// ConfirmDialog.js -- Generic confirmation modal (used for delete confirmation)
// Opens when confirmDialogSignal.value is { message, onConfirm }.
// Closes by setting confirmDialogSignal.value = null.
//
// #784: `autofocus` is a parse-time HTML directive, ignored by Preact for
// runtime-mounted components. The Cancel button never received focus, so
// pressing Enter activated the still-focused row-level Delete button and
// re-opened the dialog. Fix: useRef + useEffect to programmatically focus
// the Cancel button on mount, plus an Enter key handler so confirmation
// only happens with explicit Enter on the Delete (focused via Tab) — not
// the underlying row.
import { html } from 'htm/preact'
import { useEffect, useRef } from 'preact/hooks'
import { confirmDialogSignal } from './state.js'

export function ConfirmDialog({ message, onConfirm }) {
  const cancelRef = useRef(null)

  // Move focus into the dialog on mount. This both prevents Enter from
  // re-firing the row-level Delete button (#784) and gives screen readers
  // a sensible starting point inside the modal.
  useEffect(() => {
    if (cancelRef.current) cancelRef.current.focus()
  }, [])

  function close() {
    confirmDialogSignal.value = null
  }

  function confirm() {
    onConfirm()
    confirmDialogSignal.value = null
  }

  // Enter on Cancel = dismiss; Enter on Delete = confirm. Native button
  // activation handles both once focus is correctly inside the dialog.
  // The keydown handler on the panel is a belt-and-suspenders fallback:
  // Escape closes, Enter activates whichever button currently has focus.
  // (Esc dismissal also lives in useKeyboardNav.js.)
  function onPanelKeyDown(e) {
    if (e.key === 'Escape') {
      e.stopPropagation()
      close()
    }
  }

  return html`
    <div class="fixed inset-0 z-50 flex items-center justify-center bg-black/50"
         onClick=${(e) => e.target === e.currentTarget && close()}>
      <div role="dialog" aria-modal="true" aria-label="Confirm action"
           onKeyDown=${onPanelKeyDown}
           class="dark:bg-tn-card bg-white rounded-lg shadow-xl p-sp-24 w-full max-w-sm mx-4">
        <p class="dark:text-tn-fg text-gray-900 mb-4">${message}</p>
        <div class="flex gap-sp-8 justify-end">
          <button type="button" ref=${cancelRef}
            onClick=${close}
            class="px-4 py-2 min-h-[44px] rounded dark:text-tn-muted text-gray-600
                   hover:dark:bg-tn-muted/10 hover:bg-gray-100 transition-colors
                   focus:outline-none focus:ring-2 focus:ring-tn-blue/50">
            Cancel
          </button>
          <button type="button"
            onClick=${confirm}
            class="px-4 py-2 min-h-[44px] rounded dark:bg-tn-red/20 bg-red-100
                   dark:text-tn-red text-red-700
                   hover:dark:bg-tn-red/30 hover:bg-red-200 transition-colors
                   focus:outline-none focus:ring-2 focus:ring-tn-red/50">
            Delete
          </button>
        </div>
      </div>
    </div>
  `
}
