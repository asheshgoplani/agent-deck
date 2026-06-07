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
