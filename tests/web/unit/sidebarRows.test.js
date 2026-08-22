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

// Status chips are multi-select and bucket the same way the group stats panel
// does (statusBucket): `starting` counts as running, `queued` as idle. Before
// this, sessionMatches did an exact `statuses.includes(s.status)`, so a
// starting/queued session matched NO chip and vanished from the sidebar the
// moment any filter was activated.
describe('sessionMatches status bucketing', () => {
  const sess = (status) => ({ title: 't', group: 'g', path: '/p', tool: 'claude', branch: '', status })

  it('matches a chip on the session status itself', async () => {
    const { sessionMatches } = await import(dataModelModulePath)
    expect(sessionMatches(sess('stopped'), '', ['stopped'])).toBe(true)
    expect(sessionMatches(sess('error'), '', ['error'])).toBe(true)
    expect(sessionMatches(sess('idle'), '', ['idle'])).toBe(true)
    expect(sessionMatches(sess('running'), '', ['running'])).toBe(true)
    expect(sessionMatches(sess('waiting'), '', ['waiting'])).toBe(true)
  })

  it('folds starting into the running chip and queued into idle', async () => {
    const { sessionMatches } = await import(dataModelModulePath)
    expect(sessionMatches(sess('starting'), '', ['running'])).toBe(true)
    expect(sessionMatches(sess('queued'), '', ['idle'])).toBe(true)
  })

  it('never lets a session match every chip', async () => {
    const { sessionMatches } = await import(dataModelModulePath)
    expect(sessionMatches(sess('starting'), '', ['idle'])).toBe(false)
    expect(sessionMatches(sess('stopped'), '', ['running'])).toBe(false)
    expect(sessionMatches(sess('error'), '', ['idle'])).toBe(false)
  })

  it('still matches everything when no chip is active', async () => {
    const { sessionMatches } = await import(dataModelModulePath)
    for (const st of ['running', 'waiting', 'idle', 'error', 'stopped', 'starting', 'queued']) {
      expect(sessionMatches(sess(st), '', [])).toBe(true)
    }
  })
})
