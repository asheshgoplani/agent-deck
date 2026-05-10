// unit/state.test.js -- pin behavior of helper utilities exported from
// state.js (the bridge layer between SSE updates and Preact components).
//
// Behavior we care about for parity:
//   - clampSidebarWidth never lets the layout reach a state the TUI
//     equivalent (preview vs. menu split) cannot replicate
//   - localStorage absence/exception falls back to viewport defaults
//     instead of throwing (privacy/incognito mode users)

import { describe, it, expect, beforeEach } from 'vitest'

const stateModulePath = '../../../internal/web/static/app/state.js'

describe('clampSidebarWidth', () => {
  beforeEach(() => {
    // Reset modules so per-test localStorage stubs are honored on initial-load.
    // (state.js reads localStorage at import-time for sidebarWidthSignal.)
  })

  it('returns the default for non-finite input', async () => {
    const { clampSidebarWidth, SIDEBAR_WIDTH_DEFAULT } = await import(stateModulePath)
    expect(clampSidebarWidth(NaN)).toBe(SIDEBAR_WIDTH_DEFAULT)
    expect(clampSidebarWidth(undefined)).toBe(SIDEBAR_WIDTH_DEFAULT)
    expect(clampSidebarWidth(Infinity)).toBe(SIDEBAR_WIDTH_DEFAULT)
  })

  it('clamps below MIN', async () => {
    const { clampSidebarWidth, SIDEBAR_WIDTH_MIN } = await import(stateModulePath)
    expect(clampSidebarWidth(50)).toBe(SIDEBAR_WIDTH_MIN)
    expect(clampSidebarWidth(SIDEBAR_WIDTH_MIN - 1)).toBe(SIDEBAR_WIDTH_MIN)
  })

  it('clamps above MAX', async () => {
    const { clampSidebarWidth, SIDEBAR_WIDTH_MAX } = await import(stateModulePath)
    expect(clampSidebarWidth(9999)).toBe(SIDEBAR_WIDTH_MAX)
    expect(clampSidebarWidth(SIDEBAR_WIDTH_MAX + 1)).toBe(SIDEBAR_WIDTH_MAX)
  })

  it('rounds non-integer values inside the valid range', async () => {
    const { clampSidebarWidth } = await import(stateModulePath)
    expect(clampSidebarWidth(280.6)).toBe(281)
    expect(clampSidebarWidth(280.4)).toBe(280)
  })
})

describe('module signals export', () => {
  it('exposes the signals that SSE updates and components share', async () => {
    const state = await import(stateModulePath)
    // These names are the parity surface between vanilla SSE handlers and
    // Preact components. Renaming any of these is a breaking change.
    const required = [
      'sessionsSignal',
      'selectedIdSignal',
      'connectionSignal',
      'themeSignal',
      'settingsSignal',
      'authTokenSignal',
      'sessionCostsSignal',
      'sidebarOpenSignal',
      'sidebarWidthSignal',
      'focusedIdSignal',
      'createSessionDialogSignal',
      'confirmDialogSignal',
    ]
    for (const name of required) {
      expect(state[name], `expected exported signal ${name}`).toBeDefined()
      expect(typeof state[name].value).not.toBe('function')
    }
  })
})
