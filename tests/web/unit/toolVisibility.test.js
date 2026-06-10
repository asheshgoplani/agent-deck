// unit/toolVisibility.test.js -- tool-visibility denylist in the web UI.
//
// pickerTools from GET /api/settings is computed server-side (hidden_tools +
// show_only_installed_tools). pickerTools.js applies the web fallbacks used by
// CreateSessionDialog and EditSessionDialog.

import { describe, it, expect } from 'vitest'

const pickerToolsPath = '../../../internal/web/static/app/pickerTools.js'

describe('resolveCreateSessionPickerTools', () => {
  it('uses server pickerTools when provided (hidden tools already excluded)', async () => {
    const { resolveCreateSessionPickerTools } = await import(pickerToolsPath)
    expect(resolveCreateSessionPickerTools(['shell', 'claude', 'opencode'])).toEqual([
      'shell', 'claude', 'opencode',
    ])
  })

  it('always allows shell when the server includes it in pickerTools', async () => {
    const { resolveCreateSessionPickerTools } = await import(pickerToolsPath)
    expect(resolveCreateSessionPickerTools(['shell', 'claude'])).toContain('shell')
  })

  it('falls back to DEFAULT_PICKER_TOOLS when pickerTools is empty', async () => {
    const { resolveCreateSessionPickerTools, DEFAULT_PICKER_TOOLS } = await import(pickerToolsPath)
    expect(resolveCreateSessionPickerTools([])).toEqual(DEFAULT_PICKER_TOOLS)
  })

  it('deduplicates pickerTools', async () => {
    const { resolveCreateSessionPickerTools } = await import(pickerToolsPath)
    expect(resolveCreateSessionPickerTools(['shell', 'claude', 'shell'])).toEqual(['shell', 'claude'])
  })
})

describe('resolveEditSessionPickerTools', () => {
  it('keeps the session current tool even when absent from pickerTools', async () => {
    const { resolveEditSessionPickerTools } = await import(pickerToolsPath)
    expect(resolveEditSessionPickerTools(['shell', 'claude', 'codex'], 'gemini')).toEqual([
      'shell', 'claude', 'codex', 'gemini',
    ])
  })

  it('does not add unrelated tools outside pickerTools', async () => {
    const { resolveEditSessionPickerTools } = await import(pickerToolsPath)
    expect(resolveEditSessionPickerTools(['shell', 'claude', 'codex'], 'gemini')).not.toContain('opencode')
  })
})

describe('displayLabelForTool', () => {
  it('labels codex as ChatGPT in the picker', async () => {
    const { displayLabelForTool } = await import(pickerToolsPath)
    expect(displayLabelForTool('codex')).toBe('ChatGPT')
    expect(displayLabelForTool('claude')).toBe('claude')
  })
})
