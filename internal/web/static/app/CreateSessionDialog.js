// CreateSessionDialog.js -- Modal form for creating a new session.
// Restyled (PR-B) to use the bundle's `.dialog` / `.dh` / `.db` / `.df` /
// `.field` / `.seg-row` / `.btn` classes from app.css.
import { html } from 'htm/preact'
import { useState } from 'preact/hooks'
import {
  createSessionDialogSignal, mutationsEnabledSignal,
  toolFilterFallbackSignal, pickerToolsSignal,
} from './state.js'
import { Icon, ICONS } from './icons.js'
import { apiFetch } from './api.js'
import { displayLabelForTool, resolveCreateSessionPickerTools } from './pickerTools.js'

const CUSTOM_MODEL = '__custom__'

const REASONING_EFFORT_CATALOG = {
  claude: [
    { value: 'low', label: 'Low' },
    { value: 'medium', label: 'Medium' },
    { value: 'high', label: 'High' },
    { value: 'xhigh', label: 'Extra high' },
    { value: 'max', label: 'Max' },
  ],
  codex: [
    { value: 'minimal', label: 'Minimal' },
    { value: 'low', label: 'Low' },
    { value: 'medium', label: 'Medium' },
    { value: 'high', label: 'High' },
    { value: 'xhigh', label: 'Extra high' },
  ],
}

const MODEL_ID_CATALOG = {
  claude: [
    { value: 'claude-opus-5', label: 'Claude Opus 5' },
    { value: 'claude-sonnet-5', label: 'Claude Sonnet 5' },
    { value: 'claude-fable-5', label: 'Claude Fable 5' },
    { value: 'claude-sonnet-4-6', label: 'Claude Sonnet 4.6' },
    { value: 'claude-opus-4-8', label: 'Claude Opus 4.8' },
    { value: 'claude-opus-4-7', label: 'Claude Opus 4.7' },
    { value: 'claude-haiku-4-5', label: 'Claude Haiku 4.5 alias' },
    { value: 'claude-haiku-4-5-20251001', label: 'Claude Haiku 4.5 pinned' },
  ],
  codex: [
    { value: 'gpt-5.6-sol', label: 'GPT-5.6 Sol' },
    { value: 'gpt-5.6-terra', label: 'GPT-5.6 Terra' },
    { value: 'gpt-5.6-luna', label: 'GPT-5.6 Luna' },
    { value: 'gpt-5.5', label: 'GPT-5.5' },
    { value: 'gpt-5.5-pro', label: 'GPT-5.5 Pro' },
    { value: 'gpt-5.4', label: 'GPT-5.4' },
    { value: 'gpt-5.4-pro', label: 'GPT-5.4 Pro' },
    { value: 'gpt-5.4-mini', label: 'GPT-5.4 Mini' },
    { value: 'gpt-5.4-nano', label: 'GPT-5.4 Nano' },
    { value: 'gpt-5.3-codex', label: 'GPT-5.3 Codex' },
    { value: 'gpt-5.2', label: 'GPT-5.2' },
    { value: 'gpt-5.2-pro', label: 'GPT-5.2 Pro' },
    { value: 'gpt-5.1', label: 'GPT-5.1' },
    { value: 'gpt-5-pro', label: 'GPT-5 Pro' },
    { value: 'gpt-5', label: 'GPT-5' },
    { value: 'gpt-5-mini', label: 'GPT-5 Mini' },
    { value: 'gpt-5-nano', label: 'GPT-5 Nano' },
    { value: 'gpt-4.1', label: 'GPT-4.1' },
    { value: 'gpt-4.1-mini', label: 'GPT-4.1 Mini' },
    { value: 'gpt-4o', label: 'GPT-4o' },
    { value: 'gpt-4o-mini', label: 'GPT-4o Mini' },
    { value: 'o3-pro', label: 'o3 Pro' },
    { value: 'o3', label: 'o3' },
  ],
  gemini: [
    { value: 'gemini-3.1-pro-preview', label: 'Gemini 3.1 Pro preview' },
    { value: 'gemini-3.1-pro-preview-customtools', label: 'Gemini 3.1 Pro custom tools' },
    { value: 'gemini-3-flash-preview', label: 'Gemini 3 Flash preview' },
    { value: 'gemini-3.1-flash-lite', label: 'Gemini 3.1 Flash Lite' },
    { value: 'gemini-3.1-flash-lite-preview', label: 'Gemini 3.1 Flash Lite preview' },
    { value: 'gemini-2.5-pro', label: 'Gemini 2.5 Pro' },
    { value: 'gemini-2.5-flash', label: 'Gemini 2.5 Flash' },
    { value: 'gemini-2.5-flash-lite', label: 'Gemini 2.5 Flash Lite' },
  ],
  opencode: [
    { value: 'openai/gpt-5.5', label: 'OpenAI GPT-5.5' },
    { value: 'openai/gpt-5.5-pro', label: 'OpenAI GPT-5.5 Pro' },
    { value: 'openai/gpt-5.4', label: 'OpenAI GPT-5.4' },
    { value: 'openai/gpt-5.4-pro', label: 'OpenAI GPT-5.4 Pro' },
    { value: 'openai/gpt-5.4-mini', label: 'OpenAI GPT-5.4 Mini' },
    { value: 'openai/gpt-5.3-codex', label: 'OpenAI GPT-5.3 Codex' },
    { value: 'openai/gpt-5', label: 'OpenAI GPT-5' },
    { value: 'openai/o3', label: 'OpenAI o3' },
    { value: 'anthropic/claude-opus-5', label: 'Anthropic Claude Opus 5' },
    { value: 'anthropic/claude-sonnet-5', label: 'Anthropic Claude Sonnet 5' },
    { value: 'anthropic/claude-fable-5', label: 'Anthropic Claude Fable 5' },
    { value: 'anthropic/claude-sonnet-4-6', label: 'Anthropic Claude Sonnet 4.6' },
    { value: 'anthropic/claude-opus-4-8', label: 'Anthropic Claude Opus 4.8' },
    { value: 'anthropic/claude-opus-4-7', label: 'Anthropic Claude Opus 4.7' },
    { value: 'anthropic/claude-haiku-4-5', label: 'Anthropic Claude Haiku 4.5' },
  ],
}

function modelIDsForTool(tool) {
  return MODEL_ID_CATALOG[tool] || []
}

function reasoningEffortsForTool(tool) {
  return REASONING_EFFORT_CATALOG[tool] || []
}

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
    // Only prefill a model the catalog recognizes: an unknown id would render
    // as a blank <select> and, on submit, become an explicit per-session
    // override (see resolveClaudeLaunchModel, internal/session/claude.go:611).
    const known = (MODEL_ID_CATALOG[seedTool] || []).some(m => m.value === ctx.modelId)
    setModelId(known ? ctx.modelId : '')
    setCustomModel('')
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
      if (reasoningEffort) payload.reasoningEffort = reasoningEffort
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
  const modelIDs = modelIDsForTool(tool)
  const reasoningEfforts = reasoningEffortsForTool(tool)
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
              <select value=${reasoningEffort} onInput=${e => setReasoningEffort(e.target.value)}>
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
