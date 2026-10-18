// modelCatalog.js -- model and reasoning-effort choices for the new-session
// dialog (#2388).
//
// GET /api/settings carries `modelCatalog`, built server-side from the same
// session catalog the TUI dialog and `launch -capabilities` read: the models
// the installed CLI reports (Codex: `codex debug models`) merged in front of
// the built-in list. The static tables below are the fallback when the server
// sends no entry for a tool, and they supply display labels for known IDs.

export const CUSTOM_MODEL = '__custom__'

export const REASONING_EFFORT_CATALOG = {
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
    { value: 'max', label: 'Max' },
    { value: 'ultra', label: 'Ultra' },
  ],
}

export const MODEL_ID_CATALOG = {
  claude: [
    { value: 'claude-opus-5-5', label: 'Claude Opus 5.5' },
    { value: 'claude-opus-5', label: 'Claude Opus 5' },
    { value: 'claude-sonnet-5', label: 'Claude Sonnet 5' },
    { value: 'claude-fable-5-1', label: 'Claude Fable 5.1' },
    { value: 'claude-fable-5', label: 'Claude Fable 5' },
    { value: 'claude-sonnet-4-6', label: 'Claude Sonnet 4.6' },
    { value: 'claude-opus-4-8', label: 'Claude Opus 4.8' },
    { value: 'claude-opus-4-7', label: 'Claude Opus 4.7' },
    { value: 'claude-haiku-4-5', label: 'Claude Haiku 4.5 alias' },
    { value: 'claude-haiku-4-5-20251001', label: 'Claude Haiku 4.5 pinned' },
  ],
  codex: [
    { value: 'gpt-6-astra', label: 'GPT-6 Astra' },
    { value: 'gpt-6-sol', label: 'GPT-6 Sol' },
    { value: 'gpt-6-luna', label: 'GPT-6 Luna' },
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
    { value: 'anthropic/claude-opus-5-5', label: 'Anthropic Claude Opus 5.5' },
    { value: 'anthropic/claude-opus-5', label: 'Anthropic Claude Opus 5' },
    { value: 'anthropic/claude-sonnet-5', label: 'Anthropic Claude Sonnet 5' },
    { value: 'anthropic/claude-fable-5-1', label: 'Anthropic Claude Fable 5.1' },
    { value: 'anthropic/claude-fable-5', label: 'Anthropic Claude Fable 5' },
    { value: 'anthropic/claude-sonnet-4-6', label: 'Anthropic Claude Sonnet 4.6' },
    { value: 'anthropic/claude-opus-4-8', label: 'Anthropic Claude Opus 4.8' },
    { value: 'anthropic/claude-opus-4-7', label: 'Anthropic Claude Opus 4.7' },
    { value: 'anthropic/claude-haiku-4-5', label: 'Anthropic Claude Haiku 4.5' },
  ],
}

const EFFORT_LABELS = Object.fromEntries(
  Object.values(REASONING_EFFORT_CATALOG).flat().map(e => [e.value, e.label]),
)

function staticModelLabel(tool, id) {
  if (tool === 'opencode2') tool = 'opencode'
  const hit = (MODEL_ID_CATALOG[tool] || []).find(m => m.value === id)
  return hit ? hit.label : id
}

// modelOptionsForTool returns [{ value, label }] for the MODEL ID select.
export function modelOptionsForTool(tool, serverCatalog) {
  const entry = serverCatalog && serverCatalog[tool]
  if (entry && Array.isArray(entry.models) && entry.models.length > 0) {
    return entry.models.map(id => ({ value: id, label: staticModelLabel(tool, id) }))
  }
  return MODEL_ID_CATALOG[tool === 'opencode2' ? 'opencode' : tool] || []
}

// effortOptionsForTool returns [{ value, label }] for the REASONING EFFORT
// select, narrowed to the chosen model when the server knows its efforts.
export function effortOptionsForTool(tool, modelId, serverCatalog) {
  const entry = serverCatalog && serverCatalog[tool]
  if (entry && Array.isArray(entry.reasoningEfforts) && entry.reasoningEfforts.length > 0) {
    const perModel = modelId && entry.modelEfforts && entry.modelEfforts[modelId]
    const efforts = Array.isArray(perModel) && perModel.length > 0 ? perModel : entry.reasoningEfforts
    return efforts.map(value => ({ value, label: EFFORT_LABELS[value] || value }))
  }
  if (entry) return []
  return REASONING_EFFORT_CATALOG[tool] || []
}

// seedModelSelection maps a prefilled model ID onto the select state. A known
// ID selects its option; an unknown one is kept as a custom ID rather than
// dropped, because the list is a suggestion source, not an allowlist.
export function seedModelSelection(tool, modelId, serverCatalog) {
  const id = (modelId || '').trim()
  if (!id) return { modelId: '', customModel: '' }
  const known = modelOptionsForTool(tool, serverCatalog).some(m => m.value === id)
  return known ? { modelId: id, customModel: '' } : { modelId: CUSTOM_MODEL, customModel: id }
}
