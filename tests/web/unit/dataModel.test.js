import { beforeEach, describe, expect, it } from 'vitest'

const stateModulePath = '../../../internal/web/static/app/state.js'
const dataModelModulePath = '../../../internal/web/static/app/dataModel.js'

describe('menuModelSignal session projection', () => {
  beforeEach(async () => {
    const { sessionsSignal, sessionCostsSignal } = await import(stateModulePath)
    sessionsSignal.value = []
    sessionCostsSignal.value = {}
  })

  it('carries backend canFork independently of tool name', async () => {
    const { sessionsSignal } = await import(stateModulePath)
    const { menuModelSignal } = await import(dataModelModulePath)

    sessionsSignal.value = [
      {
        type: 'session',
        session: {
          id: 'oc-1',
          title: 'OpenCode forkable',
          tool: 'opencode',
          groupPath: 'default',
          canFork: true,
        },
      },
      {
        type: 'session',
        session: {
          id: 'claude-1',
          title: 'Claude not detected',
          tool: 'claude',
          groupPath: 'default',
          canFork: false,
        },
      },
    ]

    const byID = new Map(menuModelSignal.value.sessions.map((s) => [s.id, s]))
    expect(byID.get('oc-1').canFork).toBe(true)
    expect(byID.get('claude-1').canFork).toBe(false)
  })
})

describe('menuModelSignal group projection', () => {
  beforeEach(async () => {
    const { sessionsSignal, sessionCostsSignal } = await import(stateModulePath)
    sessionsSignal.value = []
    sessionCostsSignal.value = {}
  })

  it('carries defaultPath, raw name and level through to the group model', async () => {
    const { sessionsSignal } = await import(stateModulePath)
    const { menuModelSignal } = await import(dataModelModulePath)

    sessionsSignal.value = [
      { type: 'group', level: 0, group: { name: 'work', path: 'work', expanded: true, order: 0, sessionCount: 2, defaultPath: '/srv/work' } },
      { type: 'group', level: 1, group: { name: 'innotrade', path: 'work/innotrade', expanded: true, order: 1, sessionCount: 0 } },
    ]

    const byPath = new Map(menuModelSignal.value.groups.map((g) => [g.path, g]))

    expect(byPath.get('work').defaultPath).toBe('/srv/work')
    expect(byPath.get('work').name).toBe('work')
    expect(byPath.get('work').label).toBe('WORK')
    expect(byPath.get('work').level).toBe(0)

    // Unconfigured group: absent key becomes empty string, never undefined.
    expect(byPath.get('work/innotrade').defaultPath).toBe('')
    expect(byPath.get('work/innotrade').level).toBe(1)
  })

  it('gives synthesized groups the same shape as API-provided ones', async () => {
    const { sessionsSignal } = await import(stateModulePath)
    const { menuModelSignal } = await import(dataModelModulePath)

    // A session whose group never appeared as a group item.
    sessionsSignal.value = [
      { type: 'session', session: { id: 's1', title: 'orphan', groupPath: 'ghost', tool: 'claude' } },
    ]

    const ghost = menuModelSignal.value.groups.find((g) => g.path === 'ghost')
    expect(ghost).toBeDefined()
    expect(ghost.name).toBe('ghost')
    expect(ghost.defaultPath).toBe('')
    expect(ghost.level).toBe(0)
  })
})
