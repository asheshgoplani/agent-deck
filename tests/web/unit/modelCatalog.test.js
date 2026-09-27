// unit/modelCatalog.test.js -- new-session model/effort choices (#2388).
//
// /api/settings `modelCatalog` carries the lists the TUI uses (probed from the
// installed CLI where possible). modelCatalog.js prefers it and falls back to
// its built-in tables when the server sent nothing for a tool.

import { describe, it, expect } from 'vitest'

const modulePath = '../../../internal/web/static/app/modelCatalog.js'

const SERVER = {
  codex: {
    models: ['gpt-7-nova', 'gpt-6-astra'],
    reasoningEfforts: ['minimal', 'low', 'medium', 'high', 'xhigh', 'max', 'ultra', 'hyper'],
    modelEfforts: { 'gpt-7-nova': ['low', 'hyper'] },
  },
  gemini: { models: ['gemini-9'], reasoningEfforts: [] },
}

describe('modelOptionsForTool', () => {
  it('uses the server list and keeps known labels', async () => {
    const { modelOptionsForTool } = await import(modulePath)
    expect(modelOptionsForTool('codex', SERVER)).toEqual([
      { value: 'gpt-7-nova', label: 'gpt-7-nova' },
      { value: 'gpt-6-astra', label: 'GPT-6 Astra' },
    ])
  })

  it('falls back to the built-in table without a server entry', async () => {
    const { modelOptionsForTool, MODEL_ID_CATALOG } = await import(modulePath)
    expect(modelOptionsForTool('claude', SERVER)).toBe(MODEL_ID_CATALOG.claude)
    expect(modelOptionsForTool('codex', {})).toBe(MODEL_ID_CATALOG.codex)
    expect(modelOptionsForTool('shell', undefined)).toEqual([])
  })
})

describe('effortOptionsForTool', () => {
  it('narrows to the chosen model when the server knows its efforts', async () => {
    const { effortOptionsForTool } = await import(modulePath)
    expect(effortOptionsForTool('codex', 'gpt-7-nova', SERVER).map(e => e.value)).toEqual(['low', 'hyper'])
  })

  it('uses the tool-wide list for other models and labels unknown efforts by value', async () => {
    const { effortOptionsForTool } = await import(modulePath)
    const efforts = effortOptionsForTool('codex', 'gpt-6-astra', SERVER)
    expect(efforts.map(e => e.value)).toEqual(SERVER.codex.reasoningEfforts)
    expect(efforts.find(e => e.value === 'xhigh').label).toBe('Extra high')
    expect(efforts.find(e => e.value === 'hyper').label).toBe('hyper')
  })

  it('shows no efforts for a server tool without any', async () => {
    const { effortOptionsForTool } = await import(modulePath)
    expect(effortOptionsForTool('gemini', '', SERVER)).toEqual([])
  })

  it('falls back to the built-in table without a server entry', async () => {
    const { effortOptionsForTool, REASONING_EFFORT_CATALOG } = await import(modulePath)
    expect(effortOptionsForTool('claude', '', {})).toBe(REASONING_EFFORT_CATALOG.claude)
  })
})

describe('seedModelSelection', () => {
  it('selects a known id', async () => {
    const { seedModelSelection } = await import(modulePath)
    expect(seedModelSelection('codex', 'gpt-7-nova', SERVER)).toEqual({ modelId: 'gpt-7-nova', customModel: '' })
  })

  it('keeps an unknown id as a custom id instead of dropping it', async () => {
    const { seedModelSelection, CUSTOM_MODEL } = await import(modulePath)
    expect(seedModelSelection('claude', 'claude-opus-9', {})).toEqual({ modelId: CUSTOM_MODEL, customModel: 'claude-opus-9' })
  })

  it('leaves an empty id unset', async () => {
    const { seedModelSelection } = await import(modulePath)
    expect(seedModelSelection('codex', '  ', SERVER)).toEqual({ modelId: '', customModel: '' })
  })
})
