// Sub-sessions nest under their parent session in the sidebar.
//
// The web used to render them flat: projectSession hardcoded parent:null and
// the row builder gave every session the same depth. The TUI has always
// indented them (home.go:22273 indents by item.Level).
//
// The depth comes off the wire as MenuItem.Level rather than being re-derived
// from parentSessionId, because GroupTree.Flatten nests exactly ONE level:
// only sessions that are themselves top-level IN THAT GROUP get children
// attached. A grandchild, or a child whose parent lives in another group,
// falls through to the orphan branch and is emitted at top level with
// IsSubSession still true (internal/session/groups.go:749-773). A recursive
// client-side tree would disagree with the TUI about all of those.
import { beforeEach, describe, expect, it } from 'vitest'

const stateModulePath = '../../../internal/web/static/app/state.js'
const uiStateModulePath = '../../../internal/web/static/app/uiState.js'
const dataModelModulePath = '../../../internal/web/static/app/dataModel.js'

const group = (name, path) => ({
  type: 'group',
  level: path.split('/').length - 1,
  group: { name, path, expanded: true, order: 0 },
})

// level mirrors what BuildMenuSnapshot ships: groupLevel+1 top-level,
// groupLevel+2 nested.
const sess = (id, groupPath, level, extra = {}) => ({
  type: 'session',
  level,
  isSubSession: !!extra.isSubSession,
  session: { id, title: id, groupPath, tool: 'claude', status: 'idle', ...extra.session },
})

async function reset(menu) {
  const { sessionsSignal, sessionCostsSignal } = await import(stateModulePath)
  const { sidebarFilterSignal, groupExpandedSignal, statusFiltersSignal } = await import(uiStateModulePath)
  sessionCostsSignal.value = {}
  sidebarFilterSignal.value = ''
  statusFiltersSignal.value = []
  groupExpandedSignal.value = {}
  sessionsSignal.value = menu
}

describe('sub-session nesting', () => {
  beforeEach(() => reset([]))

  it('indents a sub-session one step past its parent session', async () => {
    await reset([
      group('work', 'work'),
      sess('conductor', 'work', 1),
      sess('child-a', 'work', 2, { isSubSession: true, session: { parentSessionId: 'conductor' } }),
      sess('child-b', 'work', 2, { isSubSession: true, session: { parentSessionId: 'conductor' } }),
      sess('solo', 'work', 1),
    ])
    const { sidebarRowsSignal } = await import(dataModelModulePath)

    expect(sidebarRowsSignal.value.map(r => [r.key, r.depth])).toEqual([
      ['g:work', 0],
      ['s:conductor', 1],
      ['s:child-a', 2],
      ['s:child-b', 2],
      ['s:solo', 1],
    ])
  })

  it('keeps the parent-then-children order the server emitted', async () => {
    await reset([
      group('work', 'work'),
      sess('p1', 'work', 1),
      sess('p1-kid', 'work', 2, { isSubSession: true, session: { parentSessionId: 'p1' } }),
      sess('p2', 'work', 1),
      sess('p2-kid', 'work', 2, { isSubSession: true, session: { parentSessionId: 'p2' } }),
    ])
    const { sidebarRowsSignal } = await import(dataModelModulePath)

    expect(sidebarRowsSignal.value.map(r => r.key)).toEqual([
      'g:work', 's:p1', 's:p1-kid', 's:p2', 's:p2-kid',
    ])
  })

  // The whole reason depth is taken from Level. Flatten emits an orphan at
  // groupLevel+1 even though IsSubSession is true; indenting it would put it
  // under a parent that is not on screen.
  it('does not indent an orphan whose parent is in another group', async () => {
    await reset([
      group('work', 'work'),
      sess('parent', 'work', 1),
      group('other', 'other'),
      // Server marks it a sub-session but emits it at top level.
      sess('orphan', 'other', 1, { isSubSession: true, session: { parentSessionId: 'parent' } }),
    ])
    const { sidebarRowsSignal } = await import(dataModelModulePath)

    const orphan = sidebarRowsSignal.value.find(r => r.key === 's:orphan')
    expect(orphan.depth).toBe(1)
    expect(orphan.session.isSubSession).toBe(true)
  })

  // Same reason: A -> B -> C. B nests under A, but C's parent is itself a
  // sub-session, so Flatten promotes C to top level.
  it('does not indent a grandchild the server promoted to top level', async () => {
    await reset([
      group('work', 'work'),
      sess('a', 'work', 1),
      sess('b', 'work', 2, { isSubSession: true, session: { parentSessionId: 'a' } }),
      sess('c', 'work', 1, { isSubSession: true, session: { parentSessionId: 'b' } }),
    ])
    const { sidebarRowsSignal } = await import(dataModelModulePath)

    expect(sidebarRowsSignal.value.map(r => [r.key, r.depth])).toEqual([
      ['g:work', 0], ['s:a', 1], ['s:b', 2], ['s:c', 1],
    ])
  })

  it('stacks sub-session depth on top of group nesting', async () => {
    await reset([
      group('stride', 'stride'),
      group('ws1', 'stride/ws1'),
      sess('parent', 'stride/ws1', 2),
      sess('kid', 'stride/ws1', 3, { isSubSession: true, session: { parentSessionId: 'parent' } }),
    ])
    const { sidebarRowsSignal } = await import(dataModelModulePath)

    expect(sidebarRowsSignal.value.map(r => [r.key, r.depth])).toEqual([
      ['g:stride', 0],
      ['g:stride/ws1', 1],
      ['s:parent', 2],
      ['s:kid', 3],
    ])
  })

  // A payload with no level at all (the archived feed projects sessions with
  // no MenuItem around them) must still render, directly under its group.
  it('falls back to directly-under-the-group when level is absent', async () => {
    await reset([
      group('work', 'work'),
      { type: 'session', session: { id: 'bare', title: 'bare', groupPath: 'work', status: 'idle' } },
    ])
    const { sidebarRowsSignal } = await import(dataModelModulePath)

    expect(sidebarRowsSignal.value.map(r => [r.key, r.depth])).toEqual([
      ['g:work', 0], ['s:bare', 1],
    ])
  })

  it('counts sub-sessions in their group badge', async () => {
    await reset([
      group('work', 'work'),
      sess('parent', 'work', 1),
      sess('kid', 'work', 2, { isSubSession: true, session: { parentSessionId: 'parent' } }),
    ])
    const { sidebarRowsSignal } = await import(dataModelModulePath)

    expect(sidebarRowsSignal.value[0].memberCount).toBe(2)
  })

  it('exposes the parent id on the projected session', async () => {
    await reset([
      group('work', 'work'),
      sess('parent', 'work', 1),
      sess('kid', 'work', 2, { isSubSession: true, session: { parentSessionId: 'parent' } }),
    ])
    const { menuModelSignal } = await import(dataModelModulePath)

    const kid = menuModelSignal.value.sessions.find(s => s.id === 'kid')
    expect(kid.parent).toBe('parent')
    expect(menuModelSignal.value.sessions.find(s => s.id === 'parent').parent).toBe(null)
  })
})
