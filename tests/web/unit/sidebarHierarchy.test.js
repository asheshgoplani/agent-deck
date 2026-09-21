// Nested groups in the sidebar.
//
// The server sends a FLAT, already-tree-ordered item list: BuildMenuSnapshot
// (internal/web/menu_snapshot_builder.go) walks GroupTree.Flatten(), whose
// GroupList is sorted hierarchically — parents before children, whole subtrees
// kept together (internal/session/groups.go:430-508). Depth rides along as
// MenuItem.Level (== strings.Count(path, "/")), and MenuGroup.Name is the LEAF
// segment ("ws1"), not the full path.
//
// Two consequences the client used to get wrong, both reproduced below:
//
//  1. MenuGroup.Order is a SIBLING ordinal, not a global rank. The server only
//     ever compares Order between groups that share a parent. Re-sorting the
//     flat list by Order client-side therefore interleaves unrelated subtrees —
//     redwood/ws1 lands in the middle of stride's children.
//
//  2. BuildMenuSnapshot deliberately force-expands every group before
//     flattening ("so descendants are always available client-side, even when
//     persisted state is collapsed") and reports the persisted flag separately.
//     Hiding a collapsed group's SUBGROUPS is therefore entirely the client's
//     job — and it has to walk the whole ancestor chain, not just the immediate
//     parent. That is the same rule the TUI needed for issue #1878.
import { beforeEach, describe, expect, it } from 'vitest'

const stateModulePath = '../../../internal/web/static/app/state.js'
const uiStateModulePath = '../../../internal/web/static/app/uiState.js'
const dataModelModulePath = '../../../internal/web/static/app/dataModel.js'

const group = (name, path, order) => ({
  type: 'group',
  level: path.split('/').length - 1,
  group: { name, path, expanded: true, order, sessionCount: 0 },
})
const sess = (id, groupPath) => ({
  type: 'session',
  session: { id, title: id, groupPath, tool: 'claude', status: 'idle' },
})

// The user's reported tree, in the order and with the Order values the server
// actually emits: two roots, each with leaf-named children whose Order restarts
// at 0 per parent.
//
//   stride            ws1 is stride's
//     ws1 ws2 ws3
//   redwood           ws1 is redwood's
//     ws1
const MENU = [
  group('stride', 'stride', 0),
  group('ws1', 'stride/ws1', 0),
  sess('s-a', 'stride/ws1'),
  group('ws2', 'stride/ws2', 1),
  sess('s-b', 'stride/ws2'),
  group('ws3', 'stride/ws3', 2),
  group('redwood', 'redwood', 1),
  group('ws1', 'redwood/ws1', 0),
  sess('s-c', 'redwood/ws1'),
]

describe('sidebar group hierarchy', () => {
  beforeEach(async () => {
    const { sessionsSignal, sessionCostsSignal } = await import(stateModulePath)
    const { sidebarFilterSignal, groupExpandedSignal, statusFiltersSignal } = await import(uiStateModulePath)
    sessionsSignal.value = MENU
    sessionCostsSignal.value = {}
    sidebarFilterSignal.value = ''
    groupExpandedSignal.value = {}
    statusFiltersSignal.value = []
  })

  // The bug as reported: every subgroup rendered as a sibling of the roots, and
  // redwood/ws1 sorted in between stride's children because Order was applied
  // globally.
  it('keeps each subtree contiguous under its own root', async () => {
    const { sidebarRowsSignal } = await import(dataModelModulePath)
    expect(sidebarRowsSignal.value.map(r => r.key)).toEqual([
      'g:stride',
      'g:stride/ws1', 's:s-a',
      'g:stride/ws2', 's:s-b',
      'g:stride/ws3',
      'g:redwood',
      'g:redwood/ws1', 's:s-c',
    ])
  })

  it('carries nesting depth on every row so the sidebar can indent it', async () => {
    const { sidebarRowsSignal } = await import(dataModelModulePath)
    expect(sidebarRowsSignal.value.map(r => [r.key, r.depth])).toEqual([
      ['g:stride', 0],
      ['g:stride/ws1', 1], ['s:s-a', 2],
      ['g:stride/ws2', 1], ['s:s-b', 2],
      ['g:stride/ws3', 1],
      ['g:redwood', 0],
      ['g:redwood/ws1', 1], ['s:s-c', 2],
    ])
  })

  // Collapsing a root has to take its whole subtree with it. Previously only
  // the root's own direct sessions disappeared and the subgroup headers stayed
  // behind, orphaned at the top level.
  it('hides the entire subtree when a root group is collapsed', async () => {
    const { groupExpandedSignal } = await import(uiStateModulePath)
    const { sidebarRowsSignal } = await import(dataModelModulePath)

    groupExpandedSignal.value = { stride: false }

    expect(sidebarRowsSignal.value.map(r => r.key)).toEqual([
      'g:stride',
      'g:redwood',
      'g:redwood/ws1', 's:s-c',
    ])
  })

  it('hides a nested subtree without touching its siblings', async () => {
    const { groupExpandedSignal } = await import(uiStateModulePath)
    const { sidebarRowsSignal } = await import(dataModelModulePath)

    groupExpandedSignal.value = { 'stride/ws1': false }

    expect(sidebarRowsSignal.value.map(r => r.key)).toEqual([
      'g:stride',
      'g:stride/ws1',
      'g:stride/ws2', 's:s-b',
      'g:stride/ws3',
      'g:redwood',
      'g:redwood/ws1', 's:s-c',
    ])
  })

  // A collapsed root that reports "(0)" while hiding three subgroups and two
  // sessions is worse than no badge at all. The TUI counts descendants here
  // (home.go:21265 renders groupStats[path].sessionCount, which is recursive),
  // so match it.
  it('counts descendants in the group badge, as the TUI does', async () => {
    const { sidebarRowsSignal } = await import(dataModelModulePath)
    const counts = Object.fromEntries(
      sidebarRowsSignal.value.filter(r => r.type === 'group').map(r => [r.path, r.memberCount]),
    )
    expect(counts).toEqual({
      'stride': 2,        // s-a + s-b, both held by subgroups
      'stride/ws1': 1,
      'stride/ws2': 1,
      'stride/ws3': 0,
      'redwood': 1,
      'redwood/ws1': 1,
    })
  })

  // A text filter keeps an ancestor on screen when a descendant matches —
  // otherwise the surviving row has no visible parent to sit under.
  it('keeps ancestors of a matching descendant visible under a filter', async () => {
    const { sidebarFilterSignal } = await import(uiStateModulePath)
    const { sidebarRowsSignal } = await import(dataModelModulePath)

    sidebarFilterSignal.value = 's-a'

    expect(sidebarRowsSignal.value.map(r => r.key)).toEqual([
      'g:stride', 'g:stride/ws1', 's:s-a',
    ])
  })
})

describe('isGroupOpen ancestor awareness', () => {
  it('reports a subgroup closed when any ancestor is collapsed', async () => {
    const { isGroupOpen, isGroupVisible } = await import(dataModelModulePath)
    // The group's OWN flag is unchanged by a parent collapsing — the TUI's
    // CollapseGroup never cascades either (internal/session/groups.go:589-600).
    expect(isGroupOpen({ stride: false }, 'stride/ws1')).toBe(true)
    // Reachability is the whole-chain question.
    expect(isGroupVisible({ stride: false }, 'stride/ws1')).toBe(false)
    expect(isGroupVisible({ stride: false }, 'stride/ws1/deep')).toBe(false)
    expect(isGroupVisible({ stride: false }, 'redwood/ws1')).toBe(true)
    expect(isGroupVisible({}, 'stride/ws1')).toBe(true)
  })
})

// Collapse is applied independently of the filter, so a parent kept on screen
// because a DESCENDANT matched used to render as a header with a non-zero
// count and nothing beneath it. Searching reveals matches; browsing does not.
describe('filtering into a collapsed subtree', () => {
  beforeEach(async () => {
    const { sessionsSignal, sessionCostsSignal } = await import(stateModulePath)
    const { sidebarFilterSignal, groupExpandedSignal, statusFiltersSignal } = await import(uiStateModulePath)
    sessionsSignal.value = MENU
    sessionCostsSignal.value = {}
    sidebarFilterSignal.value = ''
    groupExpandedSignal.value = {}
    statusFiltersSignal.value = []
  })

  it('reveals a match inside a collapsed subtree', async () => {
    const { sidebarFilterSignal, groupExpandedSignal } = await import(uiStateModulePath)
    const { sidebarRowsSignal } = await import(dataModelModulePath)

    groupExpandedSignal.value = { stride: false }
    sidebarFilterSignal.value = 's-a'

    expect(sidebarRowsSignal.value.map(r => r.key)).toEqual([
      'g:stride', 'g:stride/ws1', 's:s-a',
    ])
  })

  it('restores the collapse when the filter is cleared', async () => {
    const { sidebarFilterSignal, groupExpandedSignal } = await import(uiStateModulePath)
    const { sidebarRowsSignal } = await import(dataModelModulePath)

    groupExpandedSignal.value = { stride: false }
    sidebarFilterSignal.value = 's-a'
    sidebarFilterSignal.value = ''

    // The user's collapse was never written over.
    expect(sidebarRowsSignal.value.map(r => r.key)).toEqual([
      'g:stride', 'g:redwood', 'g:redwood/ws1', 's:s-c',
    ])
    expect(groupExpandedSignal.value).toEqual({ stride: false })
  })
})
