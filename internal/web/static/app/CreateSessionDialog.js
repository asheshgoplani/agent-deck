// CreateSessionDialog.js -- Modal form for creating a new session.
// Restyled (PR-B) to use the bundle's `.dialog` / `.dh` / `.db` / `.df` /
// `.field` / `.seg-row` / `.btn` classes from app.css.
import { html } from 'htm/preact'
import { useState } from 'preact/hooks'
import {
  createSessionDialogSignal, mutationsEnabledSignal,
  toolFilterFallbackSignal, pickerToolsSignal, modelCatalogSignal,
} from './state.js'
import { Icon, ICONS } from './icons.js'
import { apiFetch } from './api.js'
import { displayLabelForTool, resolveCreateSessionPickerTools } from './pickerTools.js'
import {
  CUSTOM_MODEL, effortOptionsForTool, modelOptionsForTool, seedModelSelection,
} from './modelCatalog.js'

export function CreateSessionDialog() {
  const open = createSessionDialogSignal.value
  const ctx = open || { groupPath: '', groupName: '', defaultPath: '', tool: '', modelId: '' }

  const [title, setTitle] = useState('')
  const [tool, setTool] = useState('claude')
  const [modelId, setModelId] = useState('')
  const [customModel, setCustomModel] = useState('')
  const [reasoningEffort, setReasoningEffort] = useState('')
  const [path, setPath] = useState('')
  const [error, setError] = useState(null)
  const [submitting, setSubmitting] = useState(false)
  const [seededFor, setSeededFor] = useState(null)

  // Tools actually offered by the picker (operator-filtered via hidden_tools /
  // show_only_installed_tools). Computed before the seeding effect below so
  // the seed can be checked against it — see next comment.
  const shownTools = resolveCreateSessionPickerTools(pickerToolsSignal.value)

  // Re-seed when the dialog opens for a different group. Keyed on groupPath so
  // SSE-driven re-renders never stomp edits the user is in the middle of, and
  // reopening on another group does not inherit the previous group's values.
  if (open && seededFor !== ctx.groupPath) {
    // Only seed a tool the picker actually shows: an operator-hidden tool
    // (e.g. `claude` filtered via hidden_tools) must never seed a selection
    // no button reflects, which used to submit an invisible/wrong tool on
    // create (review finding #2). Fall back to the first shown tool.
    const seedTool = shownTools.includes(ctx.tool) ? ctx.tool : shownTools[0]
    setTool(seedTool)
    setPath(ctx.defaultPath || '')
    // A known id selects its option; an unknown one (newer than this build's
    // list) is carried as a custom id instead of silently dropped (#2388).
    const seeded = seedModelSelection(seedTool, ctx.modelId, modelCatalogSignal.value)
    setModelId(seeded.modelId)
    setCustomModel(seeded.customModel)
    setReasoningEffort('')
    setTitle('')
    setError(null)
    setSeededFor(ctx.groupPath)
  }

  // WEB-P0-4 prevention layer: when mutations are disabled (server
  // webMutations=false), do not render the dialog at all. Hooks order is
  // preserved by placing this guard AFTER all useState calls.
  if (!mutationsEnabledSignal.value) return null

  async function handleSubmit(e) {
    e.preventDefault()
    setError(null)
    setSubmitting(true)
    try {
      const payload = { title, tool, projectPath: path }
      if (ctx.groupPath) payload.groupPath = ctx.groupPath
      const modelId = selectedModelId()
      if (modelId) payload.modelId = modelId
      if (effectiveEffort) payload.reasoningEffort = effectiveEffort
      await apiFetch('POST', '/api/sessions', payload)
      createSessionDialogSignal.value = null
    } catch (err) {
      setError(err.message)
    } finally {
      setSubmitting(false)
    }
  }

  function selectTool(nextTool) {
    setTool(nextTool)
    setModelId('')
    setCustomModel('')
    setReasoningEffort('')
  }

  function selectedModelId() {
    if (modelId === CUSTOM_MODEL) return customModel.trim()
    return modelId || ''
  }

  const close = () => (createSessionDialogSignal.value = null)
  const handleBackdropClick = (e) => { if (e.target === e.currentTarget) close() }
  const modelIDs = modelOptionsForTool(tool, modelCatalogSignal.value)
  const reasoningEfforts = effortOptionsForTool(tool, selectedModelId(), modelCatalogSignal.value)
  // The options change with the model (select or custom id) and with the
  // settings hydration; an effort they no longer offer is never submitted.
  const effectiveEffort = reasoningEfforts.some(e => e.value === reasoningEffort) ? reasoningEffort : ''
  const needsCustomModel = modelId === CUSTOM_MODEL
  const submitDisabled = submitting || !title || !path || (needsCustomModel && !customModel.trim())

  return html`
    <div class="overlay" onClick=${handleBackdropClick}>
      <form class="dialog" onClick=${e => e.stopPropagation()} onSubmit=${handleSubmit}>
        <div class="dh">
          <span class="kicker">NEW</span>
          <div class="t">New session</div>
          <button type="button" class="icon-btn" onClick=${close} aria-label="Close">
            <${Icon} d=${ICONS.x}/>
          </button>
        </div>
        <div class="db">
          ${ctx.groupName && html`
            <div class="field">
              <label>GROUP</label>
              <div class="ro-value" data-testid="create-session-group">${ctx.groupName}</div>
            </div>
          `}
          <div class="field">
            <label>NAME</label>
            <input autofocus required value=${title} onInput=${e => setTitle(e.target.value)} placeholder="session-name"/>
          </div>
          <div class="field">
            <label>WORKING DIR</label>
            <input required value=${path} onInput=${e => setPath(e.target.value)} placeholder="/absolute/path/to/project"/>
          </div>
          <div class="field">
            <label>TOOL</label>
            <div class="seg-row">
              ${shownTools.map(t => html`
                <button type="button" key=${t}
                        class=${`seg-btn ${tool === t ? 'on' : ''}`}
                        onClick=${() => selectTool(t)}>${displayLabelForTool(t)}</button>
              `)}
            </div>
            ${toolFilterFallbackSignal.value && html`
              <div style="font-family: var(--mono); font-size: 11px; color: var(--tn-comment, #888);
                          margin-top: 6px;">
                No tools matched PATH; showing all. Set <code>show_only_installed_tools = false</code> to silence.
              </div>
            `}
          </div>
          ${modelIDs.length > 0 && html`
            <div class="field">
              <label>MODEL ID</label>
              <select value=${modelId} onInput=${e => setModelId(e.target.value)}>
                <option value="">Tool default</option>
                ${modelIDs.map(m => html`
                  <option key=${m.value} value=${m.value}>${m.value} — ${m.label}</option>
                `)}
                <option value=${CUSTOM_MODEL}>Custom model ID…</option>
              </select>
            </div>
            ${needsCustomModel && html`
              <div class="field">
                <label>MODEL ID</label>
                <input required value=${customModel} onInput=${e => setCustomModel(e.target.value)} placeholder="provider/model-or-version"/>
              </div>
            `}
          `}
          ${reasoningEfforts.length > 0 && html`
            <div class="field">
              <label>REASONING EFFORT</label>
              <select value=${effectiveEffort} onInput=${e => setReasoningEffort(e.target.value)}>
                <option value="">Tool default</option>
                ${reasoningEfforts.map(effort => html`
                  <option key=${effort.value} value=${effort.value}>${effort.label} — ${effort.value}</option>
                `)}
              </select>
            </div>
          `}
          ${error && html`
            <div style="font-family: var(--mono); font-size: 11.5px; color: var(--tn-red); padding: 8px 10px;
                        border: 1px solid rgba(247,118,142,0.3); border-radius: 4px; background: rgba(247,118,142,0.06);">
              ${error}
            </div>
          `}
        </div>
        <div class="df">
          <button type="button" class="btn ghost" onClick=${close}>Cancel</button>
          <button type="submit" class="btn primary" disabled=${submitDisabled}>
            ${submitting ? 'Creating…' : html`Create session <span class="kbd">⏎</span>`}
          </button>
        </div>
      </form>
    </div>
  `
}
