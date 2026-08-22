import { beforeEach, describe, expect, it } from 'vitest'

const stateModulePath = '../../../internal/web/static/app/state.js'
const dataModelModulePath = '../../../internal/web/static/app/dataModel.js'

function session(id, status, groupPath = 'work') {
  return { type: 'session', session: { id, title: id, groupPath, tool: 'claude', status } }
}

describe('groupStats', () => {
  beforeEach(async () => {
    const { sessionsSignal, sessionCostsSignal } = await import(stateModulePath)
    sessionsSignal.value = []
    sessionCostsSignal.value = {}
  })

  it('emits non-zero buckets in the TUI fixed order with the TUI glyphs', async () => {
    const { sessionsSignal } = await import(stateModulePath)
    const { groupStats } = await import(dataModelModulePath)

    sessionsSignal.value = [
      { type: 'group', group: { name: 'work', path: 'work' } },
      session('e1', 'error'), session('e2', 'error'),
      session('r1', 'running'),
      session('w1', 'waiting'), session('w2', 'waiting'),
      session('st1', 'stopped'),
    ]

    const stats = groupStats('work')
    expect(stats.total).toBe(6)
    expect(stats.fragments.map((f) => [f.glyph, f.count, f.label])).toEqual([
      ['●', 1, 'running'],
      ['◐', 2, 'waiting'],
      ['■', 1, 'stopped'],
      ['✕', 2, 'error'],
    ])
  })

  it('omits zero buckets entirely', async () => {
    const { sessionsSignal } = await import(stateModulePath)
    const { groupStats } = await import(dataModelModulePath)

    sessionsSignal.value = [
      { type: 'group', group: { name: 'work', path: 'work' } },
      session('r1', 'running'),
    ]

    expect(groupStats('work').fragments.map((f) => f.id)).toEqual(['running'])
  })

  it('folds starting into running and queued into idle so counts sum to the total', async () => {
    const { sessionsSignal } = await import(stateModulePath)
    const { groupStats } = await import(dataModelModulePath)

    sessionsSignal.value = [
      { type: 'group', group: { name: 'work', path: 'work' } },
      session('a', 'running'), session('b', 'starting'),
      session('c', 'idle'), session('d', 'queued'),
      session('e', 'totally-unknown-status'),
    ]

    const stats = groupStats('work')
    const sum = stats.fragments.reduce((n, f) => n + f.count, 0)
    expect(sum).toBe(stats.total)
    expect(stats.fragments.find((f) => f.id === 'running').count).toBe(2)
    // idle absorbs queued and any unrecognized status.
    expect(stats.fragments.find((f) => f.id === 'idle').count).toBe(3)
  })

  it('counts direct members only, never subgroups', async () => {
    const { sessionsSignal } = await import(stateModulePath)
    const { groupStats } = await import(dataModelModulePath)

    sessionsSignal.value = [
      { type: 'group', group: { name: 'work', path: 'work' } },
      session('a', 'running', 'work'),
      { type: 'group', group: { name: 'innotrade', path: 'work/innotrade' } },
      session('b', 'running', 'work/innotrade'),
    ]

    expect(groupStats('work').total).toBe(1)
    expect(groupStats('work/innotrade').total).toBe(1)
  })

  it('returns an empty result for an unknown group', async () => {
    const { groupStats } = await import(dataModelModulePath)
    expect(groupStats('nope')).toEqual({ total: 0, fragments: [] })
  })
})

// The TUI builds its group tree from the FULL instance set (home.go:3540),
// so renderGroupPreview lists and counts ARCHIVED sessions. The web snapshot
// filters them out server-side (session_data_service.go:278), which is why a
// session archived from the TUI vanished from the web group panel entirely.
// groupMembers/groupStats fold the archived feed back in for the panel only —
// the sidebar still shows active sessions only, exactly as the TUI's list does.
describe('archived sessions in group membership', () => {
  beforeEach(async () => {
    const { sessionsSignal, sessionCostsSignal, archivedSessionsSignal } = await import(stateModulePath)
    sessionsSignal.value = []
    sessionCostsSignal.value = {}
    archivedSessionsSignal.value = []
  })

  it('groupMembers lists active members first, then archived, each flagged', async () => {
    const { sessionsSignal, archivedSessionsSignal } = await import(stateModulePath)
    const { groupMembers } = await import(dataModelModulePath)

    sessionsSignal.value = [
      { type: 'group', group: { name: 'work', path: 'work' } },
      session('live', 'running'),
    ]
    archivedSessionsSignal.value = [
      { id: 'parked', title: 'parked', tool: 'claude', status: 'stopped', groupPath: 'work' },
      { id: 'broke', title: 'broke', tool: 'codex', status: 'error', groupPath: 'work' },
    ]

    const members = groupMembers('work')
    expect(members.map((m) => [m.id, m.archived])).toEqual([
      ['live', false],
      ['parked', true],
      ['broke', true],
    ])
  })

  it('groupStats counts archived sessions in the total and the fragments', async () => {
    const { sessionsSignal, archivedSessionsSignal } = await import(stateModulePath)
    const { groupStats } = await import(dataModelModulePath)

    sessionsSignal.value = [
      { type: 'group', group: { name: 'work', path: 'work' } },
      session('live', 'running'),
    ]
    archivedSessionsSignal.value = [
      { id: 'parked', title: 'parked', tool: 'claude', status: 'stopped', groupPath: 'work' },
      { id: 'broke', title: 'broke', tool: 'codex', status: 'error', groupPath: 'work' },
    ]

    const stats = groupStats('work')
    expect(stats.total).toBe(3)
    expect(stats.fragments.map((f) => [f.id, f.count])).toEqual([
      ['running', 1], ['stopped', 1], ['error', 1],
    ])
  })

  it('buckets archived sessions by their own group, with no cross-group bleed', async () => {
    const { sessionsSignal, archivedSessionsSignal } = await import(stateModulePath)
    const { groupMembers } = await import(dataModelModulePath)

    sessionsSignal.value = [{ type: 'group', group: { name: 'work', path: 'work' } }]
    archivedSessionsSignal.value = [
      { id: 'a', title: 'a', tool: 'claude', status: 'stopped', groupPath: 'work' },
      { id: 'b', title: 'b', tool: 'claude', status: 'stopped', groupPath: 'personal' },
    ]

    expect(groupMembers('work').map((m) => m.id)).toEqual(['a'])
    expect(groupMembers('personal').map((m) => m.id)).toEqual(['b'])
  })

  it('leaves counts unchanged when nothing in the group is archived', async () => {
    const { sessionsSignal } = await import(stateModulePath)
    const { groupStats } = await import(dataModelModulePath)

    sessionsSignal.value = [
      { type: 'group', group: { name: 'work', path: 'work' } },
      session('live', 'running'),
    ]

    expect(groupStats('work').total).toBe(1)
  })
})
