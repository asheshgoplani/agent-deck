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
