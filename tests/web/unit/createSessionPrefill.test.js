import { beforeEach, describe, expect, it } from 'vitest'

const stateModulePath = '../../../internal/web/static/app/state.js'
const dataModelModulePath = '../../../internal/web/static/app/dataModel.js'

const MENU = [
  { type: 'group', group: { name: 'work', path: 'work', defaultPath: '/srv/work' } },
  { type: 'session', session: { id: 'old', title: 'old', groupPath: 'work', tool: 'gemini', modelId: 'gemini-2.5-pro', projectPath: '/srv/old', createdAt: '2026-01-01T00:00:00Z' } },
  { type: 'session', session: { id: 'new', title: 'new', groupPath: 'work', tool: 'codex', modelId: 'gpt-5.5', projectPath: '/srv/new', createdAt: '2026-06-01T00:00:00Z' } },
  { type: 'group', group: { name: 'personal', path: 'personal' } },
  { type: 'session', session: { id: 'p1', title: 'scratch', groupPath: 'personal', tool: 'shell', projectPath: '/home/me/scratch', createdAt: '2026-03-01T00:00:00Z' } },
  { type: 'group', group: { name: 'empty', path: 'empty' } },
]

describe('groupCreateDefaults', () => {
  beforeEach(async () => {
    const { sessionsSignal, sessionCostsSignal, selectedIdSignal, selectedGroupSignal } = await import(stateModulePath)
    sessionsSignal.value = MENU
    sessionCostsSignal.value = {}
    selectedIdSignal.value = null
    selectedGroupSignal.value = null
  })

  it('takes the folder from group config and the tool from the newest session', async () => {
    const { groupCreateDefaults } = await import(dataModelModulePath)

    expect(groupCreateDefaults('work')).toEqual({
      groupPath: 'work',
      groupName: 'work',
      defaultPath: '/srv/work',
      tool: 'codex',
      modelId: 'gpt-5.5',
    })
  })

  it('falls back to the newest session path when the group has no configured folder', async () => {
    const { groupCreateDefaults } = await import(dataModelModulePath)

    expect(groupCreateDefaults('personal')).toEqual({
      groupPath: 'personal',
      groupName: 'personal',
      defaultPath: '/home/me/scratch',
      tool: 'shell',
      modelId: '',
    })
  })

  it('returns empty defaults for a group with no sessions and no config', async () => {
    const { groupCreateDefaults } = await import(dataModelModulePath)

    expect(groupCreateDefaults('empty')).toEqual({
      groupPath: 'empty',
      groupName: 'empty',
      defaultPath: '',
      tool: '',
      modelId: '',
    })
  })

  it('returns a blank context for an unknown or empty group path', async () => {
    const { groupCreateDefaults } = await import(dataModelModulePath)

    expect(groupCreateDefaults('')).toEqual({
      groupPath: '', groupName: '', defaultPath: '', tool: '', modelId: '',
    })
    expect(groupCreateDefaults('nope').groupPath).toBe('')
  })
})

describe('currentGroupPath', () => {
  beforeEach(async () => {
    const { sessionsSignal, selectedIdSignal, selectedGroupSignal } = await import(stateModulePath)
    sessionsSignal.value = MENU
    selectedIdSignal.value = null
    selectedGroupSignal.value = null
  })

  it('prefers an explicitly selected group', async () => {
    const { selectGroup } = await import(stateModulePath)
    const { currentGroupPath } = await import(dataModelModulePath)

    selectGroup('personal')
    expect(currentGroupPath()).toBe('personal')
  })

  it('falls back to the selected session group', async () => {
    const { selectSession } = await import(stateModulePath)
    const { currentGroupPath } = await import(dataModelModulePath)

    selectSession('new')
    expect(currentGroupPath()).toBe('work')
  })

  it('returns empty when nothing is selected', async () => {
    const { currentGroupPath } = await import(dataModelModulePath)
    expect(currentGroupPath()).toBe('')
  })
})
