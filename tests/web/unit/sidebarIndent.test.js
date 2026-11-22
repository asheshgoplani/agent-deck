// The data model knowing a row's depth is only half the fix — the rail has to
// actually draw it. These assert the rendered DOM, so the indentation cannot
// regress to a flat list while sidebarHierarchy.test.js still passes.
import { beforeEach, describe, expect, it } from 'vitest'
import { render } from 'preact'
import { html } from 'htm/preact'

const stateModulePath = '../../../internal/web/static/app/state.js'
const uiStateModulePath = '../../../internal/web/static/app/uiState.js'
const sidebarModulePath = '../../../internal/web/static/app/Sidebar.js'

const group = (name, path, order) => ({
  type: 'group',
  level: path.split('/').length - 1,
  group: { name, path, expanded: true, order },
})
const sess = (id, groupPath) => ({
  type: 'session',
  session: { id, title: id, groupPath, tool: 'claude', status: 'idle' },
})

// Render with the SAME preact instance the components use (vitest.config.js
// aliases the bare specifier); @testing-library/preact pulls its own copy and
// the two instances' hook state do not line up.
function mount(vnode) {
  const container = document.createElement('div')
  document.body.appendChild(container)
  render(vnode, container)
  return container
}

const MENU = [
  group('stride', 'stride', 0),
  sess('top', 'stride'),
  group('ws1', 'stride/ws1', 0),
  sess('nested', 'stride/ws1'),
  group('deep', 'stride/ws1/deep', 0),
  sess('deeper', 'stride/ws1/deep'),
]

describe('sidebar indentation', () => {
  beforeEach(async () => {
    const { sessionsSignal, sessionCostsSignal, selectedIdSignal, selectedGroupSignal } = await import(stateModulePath)
    const { sidebarFilterSignal, groupExpandedSignal, statusFiltersSignal, showColsSignal } = await import(uiStateModulePath)
    sessionsSignal.value = MENU
    sessionCostsSignal.value = {}
    selectedIdSignal.value = null
    selectedGroupSignal.value = null
    sidebarFilterSignal.value = ''
    groupExpandedSignal.value = {}
    statusFiltersSignal.value = []
    showColsSignal.value = {}
  })

  it('sets one --depth step per level of group nesting', async () => {
    const { Sidebar } = await import(sidebarModulePath)
    const container = mount(html`<${Sidebar}/>`)

    const depthOf = (sel) => container.querySelector(sel).style.getPropertyValue('--depth')

    expect(depthOf('[data-row-key="g:stride"]')).toBe('0')
    expect(depthOf('[data-row-key="g:stride/ws1"]')).toBe('1')
    expect(depthOf('[data-row-key="g:stride/ws1/deep"]')).toBe('2')

    // A session sits at its own group's indent: the header-to-session step is
    // already in .sess's larger padding-left, so a flat deck is unchanged.
    expect(depthOf('[data-row-key="s:top"]')).toBe('0')
    expect(depthOf('[data-row-key="s:nested"]')).toBe('1')
    expect(depthOf('[data-row-key="s:deeper"]')).toBe('2')
  })

  it('labels a subgroup by its leaf name, as the TUI does', async () => {
    const { Sidebar } = await import(sidebarModulePath)
    const container = mount(html`<${Sidebar}/>`)

    expect(container.querySelector('[data-row-key="g:stride/ws1"] .name').textContent).toBe('WS1')
  })

  it('drops the whole subtree from the DOM when an ancestor collapses', async () => {
    const { groupExpandedSignal } = await import(uiStateModulePath)
    const { Sidebar } = await import(sidebarModulePath)

    groupExpandedSignal.value = { stride: false }
    const container = mount(html`<${Sidebar}/>`)

    expect(container.querySelector('[data-row-key="g:stride"]')).not.toBeNull()
    for (const key of ['g:stride/ws1', 'g:stride/ws1/deep', 's:top', 's:nested', 's:deeper']) {
      expect(container.querySelector(`[data-row-key="${key}"]`)).toBeNull()
    }
    // ...and the header owns up to what it is hiding.
    expect(container.querySelector('[data-row-key="g:stride"] .badge').textContent).toBe('(3)')
  })
})

// Only a session nested under ANOTHER SESSION gets the continuation guide.
// s.isSubSession alone would be wrong: the server also flags orphans whose
// parent lives in another group, and emits those at top level, where there is
// nothing above for the guide to connect to.
describe('sub-session guide', () => {
  const menu = [
    { type: 'group', level: 0, group: { name: 'stride', path: 'stride', expanded: true, order: 0 } },
    { type: 'session', level: 1, session: { id: 'parent', title: 'parent', groupPath: 'stride', status: 'idle' } },
    {
      type: 'session', level: 2, isSubSession: true,
      session: { id: 'kid', title: 'kid', groupPath: 'stride', status: 'idle', parentSessionId: 'parent' },
    },
    { type: 'group', level: 1, group: { name: 'ws1', path: 'stride/ws1', expanded: true, order: 0 } },
    // Deeper group, but NOT nested under a session — no guide.
    { type: 'session', level: 2, session: { id: 'ingroup', title: 'ingroup', groupPath: 'stride/ws1', status: 'idle' } },
    // Flagged a sub-session by the server but emitted at top level (orphan).
    {
      type: 'session', level: 2, isSubSession: true,
      session: { id: 'orphan', title: 'orphan', groupPath: 'stride/ws1', status: 'idle', parentSessionId: 'elsewhere' },
    },
  ]

  it('marks only rows indented past their own group', async () => {
    const { sessionsSignal, sessionCostsSignal, selectedIdSignal, selectedGroupSignal } = await import(stateModulePath)
    const { sidebarFilterSignal, groupExpandedSignal, statusFiltersSignal, showColsSignal } = await import(uiStateModulePath)
    sessionsSignal.value = menu
    sessionCostsSignal.value = {}
    selectedIdSignal.value = null
    selectedGroupSignal.value = null
    sidebarFilterSignal.value = ''
    groupExpandedSignal.value = {}
    statusFiltersSignal.value = []
    showColsSignal.value = {}

    const { Sidebar } = await import(sidebarModulePath)
    const container = mount(html`<${Sidebar}/>`)
    const hasGuide = (key) => container.querySelector(`[data-row-key="s:${key}"]`).classList.contains('sub')

    expect(hasGuide('kid')).toBe(true)      // depth 2, group depth 0
    expect(hasGuide('parent')).toBe(false)  // depth 1, group depth 0
    expect(hasGuide('ingroup')).toBe(false) // depth 2, but group depth 1
    expect(hasGuide('orphan')).toBe(false)  // isSubSession, but emitted at top level
  })
})
