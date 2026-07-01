// EditSessionDialog.js -- Modal for editing an existing session's metadata.
//
// Mirrors the TUI EditSessionDialog (internal/ui/edit_session_dialog.go):
// Title, Notes, Color, Tool, plus claude-only fields (Skip permissions, Auto
// mode, Extra args, Plugins, Channels). The server's PATCH handler in
// internal/web/handlers_sessions.go validates each field via
// session.SetField, so we surface its error messages verbatim.
//
// Closes "Edit session settings" MISSING row in tests/web/PARITY_MATRIX.md.

import { html } from 'htm/preact'
import { useState, useMemo } from 'preact/hooks'
import { editSessionDialogSignal, mutationsEnabledSignal, pickerToolsSignal } from './state.js'
import { menuModelSignal } from './dataModel.js'
import { Icon, ICONS } from './icons.js'
import { apiFetch } from './api.js'
import { displayLabelForTool, resolveEditSessionPickerTools } from './pickerTools.js'
import { envRowsToList, listToEnvRows } from './CreateSessionDialog.js'

// Build PATCH body from form state. Only includes fields that differ from
// the original — mirrors the TUI EditSessionDialog.GetChanges diff logic so
// no-op submits don't churn the server (or trigger restart prompts).
function diffUpdates(form, original) {
  const out = {}
  if (form.title !== original.title) out.title = form.title
  if (form.notes !== (original.notes || '')) out.notes = form.notes
  if (form.color !== (original.color || '')) out.color = form.color
  if (form.tool !== (original.tool || '')) out.tool = form.tool
  // Per-session env applies to every tool. Diff the full desired list against
  // the seeded list; send the whole replacement only when it changed.
  const nextEnv = envRowsToList(form.envRows)
  const prevEnv = original.env || []
  if (JSON.stringify(nextEnv) !== JSON.stringify(prevEnv)) out.env = nextEnv
  if (form.tool === 'claude') {
    if (form.extraArgs !== (original.extraArgs || '')) out.extraArgs = form.extraArgs
    if (form.plugins !== (original.plugins || '')) out.plugins = form.plugins
    if (form.channels !== (original.channels || '')) out.channels = form.channels
    if (form.skipPermissions !== !!original.skipPermissions) out.skipPermissions = form.skipPermissions
    if (form.autoMode !== !!original.autoMode) out.autoMode = form.autoMode
  }
  return out
}

export function EditSessionDialog() {
  const open = editSessionDialogSignal.value
  // Hooks must run unconditionally — see CreateSessionDialog.js for the same
  // pattern (state first, guards after).
  const { sessions } = menuModelSignal.value
  const session = useMemo(
    () => (open ? sessions.find(s => s.id === open.sessionId) : null),
    [open && open.sessionId, sessions],
  )
  const seed = session || { title: '', notes: '', color: '', tool: 'claude' }

  const [title, setTitle] = useState(seed.title)
  const [notes, setNotes] = useState(seed.notes || '')
  const [color, setColor] = useState(seed.color || '')
  const [tool, setTool] = useState(seed.tool || 'claude')
  const [extraArgs, setExtraArgs] = useState(seed.extraArgs || '')
  const [plugins, setPlugins] = useState(seed.plugins || '')
  const [channels, setChannels] = useState(seed.channels || '')
  const [skipPermissions, setSkipPermissions] = useState(!!seed.skipPermissions)
  const [autoMode, setAutoMode] = useState(!!seed.autoMode)
  const [envRows, setEnvRows] = useState(listToEnvRows(seed.env))
  const [error, setError] = useState(null)
  const [submitting, setSubmitting] = useState(false)
  const [seededFor, setSeededFor] = useState(open ? open.sessionId : null)

  // Re-seed when the dialog opens for a different session. Form state in a
  // closed dialog would otherwise leak into the next opening (e.g. user
  // typed in a title for sess-001, closed, reopened on sess-002).
  if (open && session && seededFor !== open.sessionId) {
    setTitle(session.title || '')
    setNotes(session.notes || '')
    setColor(session.color || '')
    setTool(session.tool || 'claude')
    setExtraArgs(session.extraArgs || '')
    setPlugins(session.plugins || '')
    setChannels(session.channels || '')
    setSkipPermissions(!!session.skipPermissions)
    setAutoMode(!!session.autoMode)
    setEnvRows(listToEnvRows(session.env))
    setError(null)
    setSeededFor(open.sessionId)
  }

  if (!open || !mutationsEnabledSignal.value || !session) return null

  const currentTool = session.tool || ''
  const shownTools = resolveEditSessionPickerTools(pickerToolsSignal.value, currentTool)

  async function handleSubmit(e) {
    e.preventDefault()
    setError(null)
    const updates = diffUpdates(
      { title, notes, color, tool, extraArgs, plugins, channels, skipPermissions, autoMode, envRows },
      session,
    )
    if (Object.keys(updates).length === 0) {
      close()
      return
    }
    setSubmitting(true)
    try {
      await apiFetch('PATCH', `/api/sessions/${encodeURIComponent(session.id)}`, updates)
      close()
    } catch (err) {
      setError(err.message || String(err))
    } finally {
      setSubmitting(false)
    }
  }

  function close() {
    editSessionDialogSignal.value = null
    setSeededFor(null)
  }

  function addEnvRow() { setEnvRows([...envRows, { key: '', value: '' }]) }
  function removeEnvRow(i) { setEnvRows(envRows.filter((_, idx) => idx !== i)) }
  function updateEnvRow(i, field, val) {
    setEnvRows(envRows.map((r, idx) => (idx === i ? { ...r, [field]: val } : r)))
  }
  const handleBackdropClick = (e) => { if (e.target === e.currentTarget) close() }
  const submitDisabled = submitting || !title.trim()

  return html`
    <div class="overlay" onClick=${handleBackdropClick} data-testid="edit-session-dialog">
      <form class="dialog" onClick=${e => e.stopPropagation()} onSubmit=${handleSubmit}>
        <div class="dh">
          <span class="kicker">EDIT</span>
          <div class="t">Edit session</div>
          <button type="button" class="icon-btn" onClick=${close} aria-label="Close">
            <${Icon} d=${ICONS.x}/>
          </button>
        </div>
        <div class="db">
          <div class="field">
            <label>TITLE</label>
            <input
              autofocus required
              data-testid="edit-session-title"
              value=${title}
              onInput=${e => setTitle(e.target.value)}
              placeholder="Session title"/>
          </div>
          <div class="field">
            <label>NOTES</label>
            <input
              data-testid="edit-session-notes"
              value=${notes}
              onInput=${e => setNotes(e.target.value)}
              placeholder="Optional notes"/>
          </div>
          <div class="field">
            <label>COLOR</label>
            <input
              data-testid="edit-session-color"
              value=${color}
              onInput=${e => setColor(e.target.value)}
              placeholder="#RRGGBB, 0-255, or blank to clear"/>
          </div>
          <div class="field">
            <label>TOOL (restart required)</label>
            <div class="seg-row">
              ${shownTools.map(t => html`
                <button type="button" key=${t}
                        class=${`seg-btn ${tool === t ? 'on' : ''}`}
                        onClick=${() => setTool(t)}>${displayLabelForTool(t)}</button>
              `)}
            </div>
          </div>
          <div class="field" data-testid="edit-session-env">
            <label>ENV VARS (restart required)</label>
            ${envRows.map((row, i) => html`
              <div class="seg-row" key=${i} style="gap: 6px; margin-bottom: 6px;">
                <input placeholder="KEY" value=${row.key}
                       onInput=${e => updateEnvRow(i, 'key', e.target.value)} style="flex: 1;"/>
                <input placeholder="value" value=${row.value}
                       onInput=${e => updateEnvRow(i, 'value', e.target.value)} style="flex: 2;"/>
                <button type="button" class="btn ghost" onClick=${() => removeEnvRow(i)} aria-label="Remove variable">✕</button>
              </div>
            `)}
            <button type="button" class="btn ghost" onClick=${addEnvRow}>+ Add variable</button>
            <div style="font-family: var(--mono); font-size: 11px; color: var(--tn-comment, #888); margin-top: 6px;">
              Per-session env vars are stored in plaintext at rest — avoid secrets you can't rotate.
            </div>
          </div>
          ${tool === 'claude' && html`
            <div class="field">
              <label>EXTRA ARGS (restart, claude)</label>
              <input
                data-testid="edit-session-extra-args"
                value=${extraArgs}
                onInput=${e => setExtraArgs(e.target.value)}
                placeholder="--model opus --verbose"/>
            </div>
            <div class="field">
              <label>PLUGINS (restart, claude — comma-separated)</label>
              <input
                data-testid="edit-session-plugins"
                value=${plugins}
                onInput=${e => setPlugins(e.target.value)}
                placeholder="octopus,discord"/>
            </div>
            <div class="field">
              <label>CHANNELS (restart, claude — comma-separated)</label>
              <input
                data-testid="edit-session-channels"
                value=${channels}
                onInput=${e => setChannels(e.target.value)}
                placeholder="plugin:telegram@org/repo"/>
            </div>
            <div class="field">
              <label>
                <input type="checkbox"
                       data-testid="edit-session-skip-permissions"
                       checked=${skipPermissions}
                       onChange=${e => setSkipPermissions(e.target.checked)}/>
                Skip permissions (restart, claude)
              </label>
            </div>
            <div class="field">
              <label>
                <input type="checkbox"
                       data-testid="edit-session-auto-mode"
                       checked=${autoMode}
                       onChange=${e => setAutoMode(e.target.checked)}/>
                Auto mode (restart, claude)
              </label>
            </div>
          `}
          ${error && html`
            <div data-testid="edit-session-error"
                 style="font-family: var(--mono); font-size: 11.5px; color: var(--tn-red); padding: 8px 10px;
                        border: 1px solid rgba(247,118,142,0.3); border-radius: 4px; background: rgba(247,118,142,0.06);">
              ${error}
            </div>
          `}
        </div>
        <div class="df">
          <button type="button" class="btn ghost" onClick=${close}>Cancel</button>
          <button type="submit" class="btn primary"
                  data-testid="edit-session-save"
                  disabled=${submitDisabled}>
            ${submitting ? 'Saving…' : html`Save <span class="kbd">⏎</span>`}
          </button>
        </div>
      </form>
    </div>
  `
}
