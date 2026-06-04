// GroupNameDialog.js -- Modal form for creating, renaming, reparenting, or deleting a group.
// Restyled (PR-B) to use the bundle's `.dialog` chrome from app.css.
// mode: 'create'   -> POST   /api/groups
//       'rename'   -> PATCH  /api/groups/{path}  {name}
//       'reparent' -> PATCH  /api/groups/{path}  {parentPath}
//       'delete'   -> DELETE /api/groups/{path}
import { html } from 'htm/preact'
import { useState } from 'preact/hooks'
import { Icon, ICONS } from './icons.js'
import { groupNameDialogSignal } from './state.js'
import { menuModelSignal } from './dataModel.js'
import { apiFetch } from './api.js'

/**
 * Pure helper — builds the API request descriptor for a group operation.
 * Exported so unit tests can pin the contract without rendering.
 *
 * @param {'create'|'rename'|'reparent'|'delete'} mode
 * @param {{ groupPath?: string, parentPath?: string }} props  — signal payload fields
 * @param {string} value  — name (create/rename) or selected parentPath (reparent); ignored for delete
 * @returns {{ method: string, path: string, body?: object }}
 */
export function groupRequestFor(mode, props, value) {
  const enc = () => '/api/groups/' + encodeURIComponent(props.groupPath)
  switch (mode) {
    case 'create': {
      const body = { name: value }
      if (props.parentPath) body.parentPath = props.parentPath
      return { method: 'POST', path: '/api/groups', body }
    }
    case 'rename':
      return { method: 'PATCH', path: enc(), body: { name: value } }
    case 'reparent':
      return { method: 'PATCH', path: enc(), body: { parentPath: value } }
    case 'delete':
      return { method: 'DELETE', path: enc() }
    default:
      throw new Error(`Unknown group dialog mode: ${mode}`)
  }
}

export function GroupNameDialog({ mode, groupPath, currentName, parentPath, onSubmit }) {
  // All hooks unconditionally at the top (hooks-order rule).
  const [name, setName] = useState(currentName || '')
  const [parentSel, setParentSel] = useState('')
  const [error, setError] = useState(null)
  const [submitting, setSubmitting] = useState(false)

  const close = () => (groupNameDialogSignal.value = null)

  // Derived display values — branched AFTER hooks.
  const isCreate   = mode === 'create'
  const isRename   = mode === 'rename'
  const isReparent = mode === 'reparent'
  const isDelete   = mode === 'delete'

  const dialogTitle  = isCreate ? 'New group'    : isRename ? 'Rename group' : isReparent ? 'Move group' : 'Delete group'
  const kickerText   = isCreate ? (parentPath ? 'NEW SUBGROUP' : 'NEW') : isRename ? 'RENAME' : isReparent ? 'MOVE' : 'DELETE'
  const submitLabel  = isCreate ? 'Create'        : isRename ? 'Rename'       : isReparent ? 'Move'        : 'Delete'

  // For reparent: all groups except self and descendants.
  const { groups } = menuModelSignal.value
  const reparentOptions = isReparent
    ? groups.filter(g => g.path !== groupPath && !g.path.startsWith(groupPath + '/'))
    : []

  async function handleSubmit(e) {
    e.preventDefault()
    setError(null)
    setSubmitting(true)
    try {
      const value = isReparent ? parentSel : name
      const { method, path, body } = groupRequestFor(mode, { groupPath, parentPath }, value)
      await apiFetch(method, path, body)
      groupNameDialogSignal.value = null
      if (onSubmit) onSubmit()
    } catch (err) {
      setError(err.message)
    } finally {
      setSubmitting(false)
    }
  }

  // Determine whether the submit button should be disabled.
  const submitDisabled = submitting
    || (isCreate && !name)
    || (isRename && !name)
    // reparent and delete are always submittable (reparent "" = move to root is valid)

  // Danger-style kicker for delete.
  const kickerStyle = isDelete
    ? 'color: var(--tn-red); background: rgba(247,118,142,0.12);'
    : ''

  const errorBlock = error && html`
    <div style="font-family: var(--mono); font-size: 11.5px; color: var(--tn-red); padding: 8px 10px;
                border: 1px solid rgba(247,118,142,0.3); border-radius: 4px; background: rgba(247,118,142,0.06);">
      ${error}
    </div>
  `

  return html`
    <div class="overlay" onClick=${(e) => e.target === e.currentTarget && close()}>
      <form class="dialog" style="max-width: 460px;"
            onClick=${e => e.stopPropagation()}
            onSubmit=${handleSubmit}>
        <div class="dh">
          <span class="kicker" style=${kickerStyle}>${kickerText}</span>
          <div class="t">${dialogTitle}</div>
          <button type="button" class="icon-btn" onClick=${close} aria-label="Close">
            <${Icon} d=${ICONS.x}/>
          </button>
        </div>
        <div class="db">
          ${isCreate && html`
            ${parentPath && html`
              <div style="font-family: var(--mono); font-size: 11.5px; color: var(--muted); margin-bottom: 8px;">
                Parent: ${parentPath}
              </div>
            `}
            <div class="field">
              <label>NAME</label>
              <input autofocus required value=${name} onInput=${e => setName(e.target.value)} placeholder="my-group"/>
            </div>
          `}
          ${isRename && html`
            <div class="field">
              <label>NAME</label>
              <input autofocus required value=${name} onInput=${e => setName(e.target.value)} placeholder="my-group"/>
            </div>
          `}
          ${isReparent && html`
            <div class="field">
              <label>PARENT GROUP</label>
              <select autofocus value=${parentSel} onInput=${e => setParentSel(e.target.value)}>
                <option value="">(root)</option>
                ${reparentOptions.map(g => html`<option key=${g.path} value=${g.path}>${g.label || g.path}</option>`)}
              </select>
            </div>
          `}
          ${isDelete && html`
            <div style="font-family: var(--sans); color: var(--text); line-height: 1.55;">
              Delete group "${currentName}"? Its sessions and subgroups move to the default group.
            </div>
          `}
          ${errorBlock}
        </div>
        <div class="df">
          <button type="button" class="btn ghost" onClick=${close}>Cancel</button>
          <button type="submit" class=${`btn ${isDelete ? 'danger' : 'primary'}`} disabled=${submitDisabled}>
            ${submitting
              ? (isCreate ? 'Creating…' : isRename ? 'Renaming…' : isReparent ? 'Moving…' : 'Deleting…')
              : submitLabel}
          </button>
        </div>
      </form>
    </div>
  `
}
