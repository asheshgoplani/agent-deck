// unit/dataModel.test.js -- pins behavior of menuModelSignal and
// displaySessionTitle added for web↔TUI parity (Task 7).
//
// Tests cover:
//   1. displaySessionTitle truth table
//   2. Projection: new fields carried through, items array order/shape,
//      and regression guard that existing groups/sessions/byGroup are unchanged.

import { describe, it, expect, beforeEach } from 'vitest'

const dataModelPath = '../../../internal/web/static/app/dataModel.js'
const statePath = '../../../internal/web/static/app/state.js'

// ---------------------------------------------------------------------------
// 1. displaySessionTitle truth table
// ---------------------------------------------------------------------------
describe('displaySessionTitle', () => {
  it('returns taskDescription when autoName is true and taskDescription is non-empty', async () => {
    const { displaySessionTitle } = await import(dataModelPath)
    const s = { autoName: true, taskDescription: 'Refactoring auth', title: 'quick-1' }
    expect(displaySessionTitle(s)).toBe('Refactoring auth')
  })

  it('returns title when autoName is true but taskDescription is empty (cold cache)', async () => {
    const { displaySessionTitle } = await import(dataModelPath)
    const s = { autoName: true, taskDescription: '', title: 'quick-1' }
    expect(displaySessionTitle(s)).toBe('quick-1')
  })

  it('returns title when autoName is false even if taskDescription is set', async () => {
    const { displaySessionTitle } = await import(dataModelPath)
    const s = { autoName: false, taskDescription: 'Refactoring auth', title: 'my-session' }
    expect(displaySessionTitle(s)).toBe('my-session')
  })

  it('returns empty string for null session', async () => {
    const { displaySessionTitle } = await import(dataModelPath)
    expect(displaySessionTitle(null)).toBe('')
  })

  it('returns empty string for undefined session', async () => {
    const { displaySessionTitle } = await import(dataModelPath)
    expect(displaySessionTitle(undefined)).toBe('')
  })
})

// ---------------------------------------------------------------------------
// 2. Projection tests
// ---------------------------------------------------------------------------
describe('menuModelSignal projection', () => {
  // Raw MenuItem fixture: one group, one top-level session, one sub-session.
  const RAW_ITEMS = [
    {
      type: 'group',
      level: 0,
      group: {
        path: 'work',
        name: 'Work',
        expanded: true,
        sessionCount: 2,
        order: 1,
      },
    },
    {
      type: 'session',
      level: 0,
      isSubSession: false,
      session: {
        id: 'sess-top',
        title: 'Top Session',
        tool: 'claude',
        groupPath: 'work',
        projectPath: '/home/user/project',
        status: 'running',
        parentSessionId: '',
        archived: false,
        archivedAt: '',
        autoName: false,
        taskDescription: '',
        worktreeType: '',
        worktreeBranch: '',
        worktreeRepoRoot: '',
      },
    },
    {
      type: 'session',
      level: 1,
      isSubSession: true,
      session: {
        id: 'sess-sub',
        title: 'Sub Session',
        tool: 'claude',
        groupPath: 'work',
        projectPath: '/home/user/project',
        status: 'idle',
        parentSessionId: 'sess-top',
        archived: true,
        archivedAt: '2026-05-30T10:00:00Z',
        autoName: true,
        taskDescription: 'Refactoring auth',
        worktreeType: 'git',
        worktreeBranch: 'feat/auth',
        worktreeRepoRoot: '/home/user/project',
      },
    },
  ]

  beforeEach(async () => {
    // Reset sessionsSignal to the fixture before each test in this suite.
    const { sessionsSignal, sessionCostsSignal } = await import(statePath)
    sessionsSignal.value = RAW_ITEMS
    sessionCostsSignal.value = {}
  })

  it('items array preserves raw order and has correct types', async () => {
    const { menuModelSignal } = await import(dataModelPath)
    const { items } = menuModelSignal.value
    expect(items).toHaveLength(3)
    expect(items[0].type).toBe('group')
    expect(items[1].type).toBe('session')
    expect(items[2].type).toBe('session')
  })

  it('items group entry carries the correct level', async () => {
    const { menuModelSignal } = await import(dataModelPath)
    const { items } = menuModelSignal.value
    expect(items[0].level).toBe(0)
  })

  it('items top-level session carries level 0 and isSubSession false', async () => {
    const { menuModelSignal } = await import(dataModelPath)
    const { items } = menuModelSignal.value
    expect(items[1].level).toBe(0)
    expect(items[1].isSubSession).toBe(false)
  })

  it('items sub-session carries level 1 and isSubSession true', async () => {
    const { menuModelSignal } = await import(dataModelPath)
    const { items } = menuModelSignal.value
    expect(items[2].level).toBe(1)
    expect(items[2].isSubSession).toBe(true)
  })

  it('projected sub-session carries all 7 new fields correctly', async () => {
    const { menuModelSignal } = await import(dataModelPath)
    const { items } = menuModelSignal.value
    const sub = items[2].session
    expect(sub.level).toBe(1)
    expect(sub.isSubSession).toBe(true)
    expect(sub.parentSessionId).toBe('sess-top')
    expect(sub.archived).toBe(true)
    expect(sub.archivedAt).toBe('2026-05-30T10:00:00Z')
    expect(sub.autoName).toBe(true)
    expect(sub.taskDescription).toBe('Refactoring auth')
    expect(sub.worktreeType).toBe('git')
  })

  it('projected top-level session defaults new fields to safe zeros', async () => {
    const { menuModelSignal } = await import(dataModelPath)
    const { items } = menuModelSignal.value
    const top = items[1].session
    expect(top.level).toBe(0)
    expect(top.isSubSession).toBe(false)
    expect(top.parentSessionId).toBe('')
    expect(top.archived).toBe(false)
    expect(top.archivedAt).toBe('')
    expect(top.autoName).toBe(false)
    expect(top.taskDescription).toBe('')
    expect(top.worktreeType).toBe('')
  })

  // Regression guards: existing keys must still exist and be correct shape.
  it('groups key still exists and contains the expected group (regression guard)', async () => {
    const { menuModelSignal } = await import(dataModelPath)
    const { groups } = menuModelSignal.value
    expect(Array.isArray(groups)).toBe(true)
    expect(groups.length).toBeGreaterThan(0)
    const work = groups.find(g => g.path === 'work')
    expect(work).toBeDefined()
    expect(work.label).toBe('WORK')
    expect(work.sessionCount).toBe(2)
  })

  it('sessions key still exists and contains both sessions (regression guard)', async () => {
    const { menuModelSignal } = await import(dataModelPath)
    const { sessions } = menuModelSignal.value
    expect(Array.isArray(sessions)).toBe(true)
    expect(sessions).toHaveLength(2)
    const ids = sessions.map(s => s.id)
    expect(ids).toContain('sess-top')
    expect(ids).toContain('sess-sub')
  })

  it('byGroup key still exists and maps group path to sessions (regression guard)', async () => {
    const { menuModelSignal } = await import(dataModelPath)
    const { byGroup } = menuModelSignal.value
    expect(byGroup).toBeDefined()
    expect(Array.isArray(byGroup['work'])).toBe(true)
    expect(byGroup['work']).toHaveLength(2)
  })

  it('cost hydration in items matches sessions path', async () => {
    const { sessionsSignal, sessionCostsSignal } = await import(statePath)
    sessionsSignal.value = RAW_ITEMS
    sessionCostsSignal.value = { 'sess-sub': 1.23 }

    const { menuModelSignal } = await import(dataModelPath)
    const { items, sessions } = menuModelSignal.value

    // Find sub-session in items
    const subInItems = items.find(i => i.type === 'session' && i.session.id === 'sess-sub')
    const subInSessions = sessions.find(s => s.id === 'sess-sub')

    expect(subInItems.session.cost).toBe(1.23)
    expect(subInSessions.cost).toBe(1.23)
  })

  // Regression guard from upstream #1299: canFork is a backend-supplied flag
  // and must be carried through the projection independently of tool name.
  it('carries backend canFork independently of tool name', async () => {
    const { sessionsSignal } = await import(statePath)
    const { menuModelSignal } = await import(dataModelPath)

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
