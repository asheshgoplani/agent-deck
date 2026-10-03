// Defaults that existing viewers depend on: the Fleet board opens on the
// group grid unless the viewer opted into the status kanban, and a sidebar
// column option added after a viewer saved their choices starts at its
// default instead of reading as unchecked. Each test loads uiState.js fresh
// (vi.resetModules) because these are read from localStorage at import time.
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { render } from 'preact'
import { html } from 'htm/preact'

const uiStateModulePath = '../../../internal/web/static/app/uiState.js'
const stateModulePath = '../../../internal/web/static/app/state.js'
const paneModulePath = '../../../internal/web/static/app/panes/FleetPane.js'

function mount(vnode) {
  const container = document.createElement('div')
  document.body.appendChild(container)
  render(vnode, container)
  return container
}

const sess = (id, status, hints) => ({
  type: 'session',
  session: { id, title: id, groupPath: 'work', tool: 'claude', status, hints },
})

describe('Fleet board default view', () => {
  beforeEach(() => {
    localStorage.clear()
    vi.resetModules()
  })
  afterEach(() => localStorage.clear())

  it('is the group grid when the viewer never chose a layout', async () => {
    const { fleetViewSignal } = await import(uiStateModulePath)
    expect(fleetViewSignal.value).toBe('groups')

    const { sessionsSignal, sessionCostsSignal } = await import(stateModulePath)
    sessionCostsSignal.value = {}
    sessionsSignal.value = [
      { type: 'group', level: 0, group: { name: 'work', path: 'work', expanded: true, order: 0 } },
      sess('a', 'running', { status: 'needs-input' }),
      sess('b', 'waiting', {}),
      sess('c', 'idle', undefined),
    ]
    const { FleetPane } = await import(paneModulePath)
    const c = mount(html`<${FleetPane}/>`)
    await new Promise(r => setTimeout(r, 0))
    expect(c.querySelector('[data-testid="fleet-group-card"]')).not.toBeNull()
    expect(c.querySelector('[data-testid="fleet-kanban"]')).toBeNull()
    expect(c.querySelector('[data-testid="conductor-banner"]')).toBeNull()
    expect(c.querySelector('[data-testid="fleet-status-stats"]')).toBeNull()
    expect(c.querySelector('[data-testid="fleet-view-groups"]').getAttribute('aria-pressed')).toBe('true')
    // The runtime tiles are the board's stat row.
    const tile = (id) => c.querySelector(`[data-testid="fleet-stat-${id}"] .num`)?.textContent
    expect([tile('running'), tile('waiting'), tile('error'), tile('idle')]).toEqual(['1', '1', '0', '1'])
  })

  it('keeps a viewer who opted into the status kanban there', async () => {
    localStorage.setItem('agentdeck.fleetView', '"status"')
    const { fleetViewSignal } = await import(uiStateModulePath)
    expect(fleetViewSignal.value).toBe('status')
  })
})

describe('sidebar column options', () => {
  beforeEach(() => {
    localStorage.clear()
    vi.resetModules()
  })
  afterEach(() => localStorage.clear())

  it('turns the annotations option on for a showCols saved before it existed', async () => {
    localStorage.setItem('agentdeck.showCols', JSON.stringify({ tool: true, cost: false, branch: true }))
    const { showColsSignal } = await import(uiStateModulePath)
    expect(showColsSignal.value).toEqual({ annotations: true, tool: true, cost: false, branch: true })
  })

  it('keeps an explicitly saved annotations: false', async () => {
    localStorage.setItem('agentdeck.showCols', JSON.stringify({ tool: true, annotations: false }))
    const { showColsSignal } = await import(uiStateModulePath)
    expect(showColsSignal.value.annotations).toBe(false)
  })

  it('defaults every option when nothing was saved', async () => {
    const { showColsSignal } = await import(uiStateModulePath)
    expect(showColsSignal.value).toEqual({
      annotations: true, tool: true, cost: true, branch: false, attach: false, sandbox: false, lastSeen: false,
    })
  })
})
