import { beforeEach, describe, expect, it } from 'vitest'

const stateModulePath = '../../../internal/web/static/app/state.js'
const uiStateModulePath = '../../../internal/web/static/app/uiState.js'
const dataModelModulePath = '../../../internal/web/static/app/dataModel.js'

const MENU = [
  { type: 'group', level: 0, group: { name: 'work', path: 'work', expanded: true, order: 0 } },
  { type: 'session', session: { id: 's1', title: 'api', groupPath: 'work', tool: 'claude', status: 'running' } },
  { type: 'session', session: { id: 's2', title: 'web', groupPath: 'work', tool: 'codex', status: 'idle' } },
  { type: 'group', level: 0, group: { name: 'personal', path: 'personal', expanded: true, order: 1 } },
  { type: 'session', session: { id: 's3', title: 'scratch', groupPath: 'personal', tool: 'shell', status: 'idle' } },
]

describe('sidebarRowsSignal', () => {
  beforeEach(async () => {
    const { sessionsSignal, sessionCostsSignal } = await import(stateModulePath)
    const { sidebarFilterSignal, groupExpandedSignal, statusFiltersSignal } = await import(uiStateModulePath)
    sessionsSignal.value = MENU
    sessionCostsSignal.value = {}
    sidebarFilterSignal.value = ''
    groupExpandedSignal.value = {}
    statusFiltersSignal.value = []
  })

  it('interleaves group headers with their members in render order', async () => {
    const { sidebarRowsSignal } = await import(dataModelModulePath)
    expect(sidebarRowsSignal.value.map((r) => r.key)).toEqual([
      'g:work', 's:s1', 's:s2', 'g:personal', 's:s3',
    ])
  })

  it('omits members of a collapsed group but keeps its header', async () => {
    const { groupExpandedSignal } = await import(uiStateModulePath)
    const { sidebarRowsSignal } = await import(dataModelModulePath)

    groupExpandedSignal.value = { work: false }

    expect(sidebarRowsSignal.value.map((r) => r.key)).toEqual([
      'g:work', 'g:personal', 's:s3',
    ])
    // The header still reports how many members it is hiding.
    expect(sidebarRowsSignal.value[0].memberCount).toBe(2)
  })

  it('drops groups with no matching members when a text filter is active', async () => {
    const { sidebarFilterSignal } = await import(uiStateModulePath)
    const { sidebarRowsSignal } = await import(dataModelModulePath)

    sidebarFilterSignal.value = 'scratch'

    expect(sidebarRowsSignal.value.map((r) => r.key)).toEqual(['g:personal', 's:s3'])
  })

  it('honors status filter chips', async () => {
    const { statusFiltersSignal } = await import(uiStateModulePath)
    const { sidebarRowsSignal } = await import(dataModelModulePath)

    statusFiltersSignal.value = ['running']

    expect(sidebarRowsSignal.value.map((r) => r.key)).toEqual(['g:work', 's:s1', 'g:personal'])
  })
})

describe('groupExpandedSignal persistence', () => {
  it('writes collapse state to localStorage', async () => {
    const { groupExpandedSignal } = await import(uiStateModulePath)
    groupExpandedSignal.value = { work: false }
    expect(JSON.parse(localStorage.getItem('agentdeck.groupExpanded'))).toEqual({ work: false })
  })
})

describe('isGroupOpen', () => {
  it('treats an absent entry as open, matching the pre-existing predicate', async () => {
    const { isGroupOpen } = await import(dataModelModulePath)
    expect(isGroupOpen({}, 'work')).toBe(true)
    expect(isGroupOpen({ work: true }, 'work')).toBe(true)
    expect(isGroupOpen({ work: false }, 'work')).toBe(false)
  })
})
