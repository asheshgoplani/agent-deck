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

const MODEL_ID_CATALOG = {
  claude: [
    { value: 'claude-sonnet-4-6', label: 'Claude Sonnet 4.6' },
    { value: 'claude-opus-4-8', label: 'Claude Opus 4.8' },
    { value: 'claude-opus-4-7', label: 'Claude Opus 4.7' },
    { value: 'claude-haiku-4-5', label: 'Claude Haiku 4.5 alias' },
    { value: 'claude-haiku-4-5-20251001', label: 'Claude Haiku 4.5 pinned' },
  ],
  codex: [
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
    { value: 'anthropic/claude-sonnet-4-6', label: 'Anthropic Claude Sonnet 4.6' },
    { value: 'anthropic/claude-opus-4-8', label: 'Anthropic Claude Opus 4.8' },
    { value: 'anthropic/claude-opus-4-7', label: 'Anthropic Claude Opus 4.7' },
    { value: 'anthropic/claude-haiku-4-5', label: 'Anthropic Claude Haiku 4.5' },
  ],
}

function modelIDsForTool(tool) {
  return MODEL_ID_CATALOG[tool] || []
}

// envRowsToList collapses the {key,value} editor rows into the wire format the
// API expects (["KEY=VALUE", …]), dropping rows with a blank key.
export function envRowsToList(rows) {
  return rows
    .map(r => ({ key: (r.key || '').trim(), value: r.value || '' }))
    .filter(r => r.key !== '')
    .map(r => `${r.key}=${r.value}`)
}

// listToEnvRows parses a ["KEY=VALUE", …] list (as seeded from MenuSession.env)
// back into editor rows, splitting on the first '='.
export function listToEnvRows(list) {
  return (list || []).map(kv => {
    const i = kv.indexOf('=')
    return i < 0 ? { key: kv, value: '' } : { key: kv.slice(0, i), value: kv.slice(i + 1) }
  })
}

export function CreateSessionDialog() {
  const [title, setTitle] = useState('')
  const [tool, setTool] = useState('claude')
  const [modelId, setModelId] = useState('')
  const [customModel, setCustomModel] = useState('')
  const [path, setPath] = useState('')
  const [envRows, setEnvRows] = useState([])
  const [error, setError] = useState(null)
  const [submitting, setSubmitting] = useState(false)

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
      const modelId = selectedModelId()
      if (modelId) payload.modelId = modelId
      const env = envRowsToList(envRows)
      if (env.length) payload.env = env
      await apiFetch('POST', '/api/sessions', payload)
      createSessionDialogSignal.value = false
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
  }

  function selectedModelId() {
    if (modelId === CUSTOM_MODEL) return customModel.trim()
    return modelId || ''
  }

  function addEnvRow() { setEnvRows([...envRows, { key: '', value: '' }]) }
  function removeEnvRow(i) { setEnvRows(envRows.filter((_, idx) => idx !== i)) }
  function updateEnvRow(i, field, val) {
    setEnvRows(envRows.map((r, idx) => (idx === i ? { ...r, [field]: val } : r)))
  }

  const close = () => (createSessionDialogSignal.value = false)
  const handleBackdropClick = (e) => { if (e.target === e.currentTarget) close() }
  const modelIDs = modelIDsForTool(tool)
  const shownTools = resolveCreateSessionPickerTools(pickerToolsSignal.value)
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
          <div class="field">
            <label>TITLE</label>
            <input autofocus required value=${title} onInput=${e => setTitle(e.target.value)} placeholder="my-session"/>
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
          <div class="field" data-testid="create-session-env">
            <label>ENV VARS</label>
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
