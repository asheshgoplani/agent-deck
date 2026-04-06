// ConfirmDialog.js -- Generic confirmation modal (used for delete confirmation)
// Opens when confirmDialogSignal.value is { message, onConfirm }.
// Closes by setting confirmDialogSignal.value = null.
import { html } from 'htm/preact'
import { confirmDialogSignal } from './state.js'

export function ConfirmDialog({ message, onConfirm }) {
  return html`
    <div class="fixed inset-0 z-50 flex items-center justify-center bg-black/50">
      <div class="dark:bg-tn-card bg-white rounded-lg shadow-xl p-sp-24 w-full max-w-sm mx-4">
        <p class="dark:text-tn-fg text-gray-900 mb-4">${message}</p>
        <div class="flex gap-sp-8 justify-end">
          <button type="button" autofocus
            onClick=${() => (confirmDialogSignal.value = null)}
            class="px-4 py-2 min-h-[44px] rounded dark:text-tn-muted text-gray-600
                   hover:dark:bg-tn-muted/10 hover:bg-gray-100 transition-colors">
            Cancel
          </button>
          <button type="button"
            onClick=${() => { onConfirm(); confirmDialogSignal.value = null }}
            class="px-4 py-2 min-h-[44px] rounded dark:bg-tn-red/20 bg-red-100
                   dark:text-tn-red text-red-700
                   hover:dark:bg-tn-red/30 hover:bg-red-200 transition-colors">
            Delete
          </button>
        </div>
      </div>
    </div>
  `
}
