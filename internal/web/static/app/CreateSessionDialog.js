// CreateSessionDialog.js -- Modal form for creating a new session
// Opens when createSessionDialogSignal.value === true.
// Submits POST /api/sessions with { title, tool, projectPath }.
import { html } from 'htm/preact'
import { useState } from 'preact/hooks'
import { createSessionDialogSignal, mutationsEnabledSignal } from './state.js'
import { apiFetch } from './api.js'

export function CreateSessionDialog() {
  const [title, setTitle] = useState('')
  const [tool, setTool] = useState('claude')
  const [path, setPath] = useState('')
  const [error, setError] = useState(null)
  const [submitting, setSubmitting] = useState(false)

  // WEB-P0-4 prevention layer: when mutations are disabled (server
  // webMutations=false), do not render the dialog at all. The caller
  // that sets createSessionDialogSignal.value = true should already be
  // gated on the same signal, but this is a defensive guard in case
  // the dialog is opened via some other path (keyboard shortcut,
  // direct signal manipulation, etc.). Placed AFTER the useState hooks
  // so Preact's hook order stays stable across re-renders (rules-of-hooks).
  if (!mutationsEnabledSignal.value) return null

  async function handleSubmit(e) {
    e.preventDefault()
    setError(null)
    setSubmitting(true)
    try {
      await apiFetch('POST', '/api/sessions', { title, tool, projectPath: path })
      createSessionDialogSignal.value = false
    } catch (err) {
      setError(err.message)
    } finally {
      setSubmitting(false)
    }
  }

  function handleBackdropClick(e) {
    if (e.target === e.currentTarget) createSessionDialogSignal.value = false
  }

  return html`
    <div class="fixed inset-0 z-50 flex items-center justify-center bg-black/50"
         onClick=${handleBackdropClick}>
      <div class="dark:bg-tn-card bg-white rounded-lg shadow-xl p-sp-24 w-full max-w-md mx-4">
        <h2 class="text-lg font-semibold dark:text-tn-fg text-gray-900 mb-4">New Session</h2>
        <form onSubmit=${handleSubmit} class="flex flex-col gap-sp-12">
          <input autofocus required
            placeholder="Session title"
            value=${title} onInput=${e => setTitle(e.target.value)}
            class="w-full px-3 py-2 min-h-[44px] rounded border dark:border-tn-muted/30 dark:bg-tn-bg dark:text-tn-fg
                   bg-gray-50 text-gray-900 border-gray-300
                   focus:outline-none focus:ring-2 focus:ring-tn-blue/50" />
          <input required
            placeholder="Working directory (absolute path)"
            value=${path} onInput=${e => setPath(e.target.value)}
            class="w-full px-3 py-2 min-h-[44px] rounded border dark:border-tn-muted/30 dark:bg-tn-bg dark:text-tn-fg
                   bg-gray-50 text-gray-900 border-gray-300
                   focus:outline-none focus:ring-2 focus:ring-tn-blue/50" />
          <select value=${tool} onChange=${e => setTool(e.target.value)}
            class="w-full px-3 py-2 min-h-[44px] rounded border dark:border-tn-muted/30 dark:bg-tn-bg dark:text-tn-fg
                   bg-gray-50 text-gray-900 border-gray-300">
            <option value="claude">claude</option>
            <option value="shell">shell</option>
            <option value="gemini">gemini</option>
            <option value="codex">codex</option>
          </select>
          ${error && html`<p class="text-sm dark:text-tn-red text-red-600">${error}</p>`}
          <div class="flex gap-sp-8 justify-end mt-2">
            <button type="button"
              onClick=${() => (createSessionDialogSignal.value = false)}
              class="px-4 py-2 min-h-[44px] rounded dark:text-tn-muted text-gray-600
                     hover:dark:bg-tn-muted/10 hover:bg-gray-100 transition-colors">
              Cancel
            </button>
            <button type="submit" disabled=${submitting || !mutationsEnabledSignal.value}
              class="px-4 py-2 min-h-[44px] rounded dark:bg-tn-blue/20 bg-blue-100
                     dark:text-tn-blue text-blue-700
                     hover:dark:bg-tn-blue/30 hover:bg-blue-200 transition-colors
                     disabled:opacity-50">
              ${submitting ? 'Creating...' : 'Create'}
            </button>
          </div>
        </form>
      </div>
    </div>
  `
}
