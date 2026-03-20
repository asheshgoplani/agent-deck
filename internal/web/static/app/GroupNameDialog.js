// GroupNameDialog.js -- Modal form for creating or renaming a group
// Opens when groupNameDialogSignal.value is { mode, groupPath, currentName, onSubmit }.
// mode: 'create' -> POST /api/groups, 'rename' -> PATCH /api/groups/{path}
import { html } from 'htm/preact'
import { useState } from 'preact/hooks'
import { groupNameDialogSignal } from './state.js'
import { apiFetch } from './api.js'

export function GroupNameDialog({ mode, groupPath, currentName, onSubmit }) {
  const [name, setName] = useState(currentName || '')
  const [error, setError] = useState(null)
  const [submitting, setSubmitting] = useState(false)

  const isCreate = mode === 'create'
  const dialogTitle = isCreate ? 'New Group' : 'Rename Group'
  const submitLabel = isCreate ? 'Create' : 'Rename'

  async function handleSubmit(e) {
    e.preventDefault()
    setError(null)
    setSubmitting(true)
    try {
      if (isCreate) {
        await apiFetch('POST', '/api/groups', { name })
      } else {
        await apiFetch('PATCH', '/api/groups/' + encodeURIComponent(groupPath), { name })
      }
      groupNameDialogSignal.value = null
      if (onSubmit) onSubmit()
    } catch (err) {
      setError(err.message)
    } finally {
      setSubmitting(false)
    }
  }

  return html`
    <div class="fixed inset-0 z-50 flex items-center justify-center bg-black/50"
         onClick=${(e) => e.target === e.currentTarget && (groupNameDialogSignal.value = null)}>
      <div class="dark:bg-tn-card bg-white rounded-lg shadow-xl p-sp-24 w-full max-w-sm mx-4">
        <h2 class="text-lg font-semibold dark:text-tn-fg text-gray-900 mb-4">${dialogTitle}</h2>
        <form onSubmit=${handleSubmit} class="flex flex-col gap-sp-12">
          <input autofocus required
            placeholder="Group name"
            value=${name} onInput=${e => setName(e.target.value)}
            class="w-full px-3 py-2 rounded border dark:border-tn-muted/30 dark:bg-tn-bg dark:text-tn-fg
                   bg-gray-50 text-gray-900 border-gray-300
                   focus:outline-none focus:ring-2 focus:ring-tn-blue/50" />
          ${error && html`<p class="text-sm dark:text-tn-red text-red-600">${error}</p>`}
          <div class="flex gap-sp-8 justify-end mt-2">
            <button type="button"
              onClick=${() => (groupNameDialogSignal.value = null)}
              class="px-4 py-2 rounded dark:text-tn-muted text-gray-600
                     hover:dark:bg-tn-muted/10 hover:bg-gray-100 transition-colors">
              Cancel
            </button>
            <button type="submit" disabled=${submitting}
              class="px-4 py-2 rounded dark:bg-tn-blue/20 bg-blue-100
                     dark:text-tn-blue text-blue-700
                     hover:dark:bg-tn-blue/30 hover:bg-blue-200 transition-colors
                     disabled:opacity-50">
              ${submitting ? (isCreate ? 'Creating...' : 'Renaming...') : submitLabel}
            </button>
          </div>
        </form>
      </div>
    </div>
  `
}
