// GroupNameDialog.js -- Modal form for creating or renaming a group.
// Restyled (PR-B) to use the bundle's `.dialog` chrome from app.css.
// mode: 'create' -> POST /api/groups, 'rename' -> PATCH /api/groups/{path}
import { html } from 'htm/preact'
import { useState } from 'preact/hooks'
import { Icon, ICONS } from './icons.js'
import { groupNameDialogSignal } from './state.js'
import { apiFetch } from './api.js'

export function GroupNameDialog({ mode, groupPath, currentName, onSubmit }) {
  const [name, setName] = useState(currentName || '')
  const [error, setError] = useState(null)
  const [submitting, setSubmitting] = useState(false)

  const isCreate = mode === 'create'
  const dialogTitle = isCreate ? 'New group' : 'Rename group'
  const submitLabel = isCreate ? 'Create' : 'Rename'
  const close = () => (groupNameDialogSignal.value = null)

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
    <div class="overlay" onClick=${(e) => e.target === e.currentTarget && close()}>
      <form class="dialog" style="max-width: 460px;"
            onClick=${e => e.stopPropagation()}
            onSubmit=${handleSubmit}>
        <div class="dh">
          <span class="kicker">${isCreate ? 'NEW' : 'RENAME'}</span>
          <div class="t">${dialogTitle}</div>
          <button type="button" class="icon-btn" onClick=${close} aria-label="Close">
            <${Icon} d=${ICONS.x}/>
          </button>
        </div>
        <div class="db">
          <div class="field">
            <label>NAME</label>
            <input autofocus required value=${name} onInput=${e => setName(e.target.value)} placeholder="my-group"/>
          </div>
          ${error && html`
            <div style="font-family: var(--mono); font-size: 11.5px; color: var(--tn-red); padding: 8px 10px;
                        border: 1px solid rgba(247,118,142,0.3); border-radius: 4px; background: rgba(247,118,142,0.06);">
              ${error}
            </div>
          `}
        </div>
        <div class="df">
          <button type="button" class="btn ghost" onClick=${close}>Cancel</button>
          <button type="submit" class="btn primary" disabled=${submitting || !name}>
            ${submitting ? (isCreate ? 'Creating…' : 'Renaming…') : submitLabel}
          </button>
        </div>
      </form>
    </div>
  `
}
