// unit/sidebar.test.js -- render-based tests for the nested Sidebar (Task 8).
//
// This is the only automated verification of the Sidebar's render + interaction
// behavior (no snapshots / Playwright here), so it exercises real DOM:
//   1. Nesting: group + L0 session + L1 sub-session render with increasing indent.
//   2. Recursive collapse: collapsing a parent group hides a nested subgroup's
//      session (descendants disappear from the DOM).
//   3. Archived filter: hidden when showArchived=false; shown + badged when true.
//   4. displaySessionTitle: an autoName session renders its taskDescription.
//   5. Action wiring: Archive fires POST /archive; Edit sets editSessionDialogSignal;
//      a group affordance sets groupNameDialogSignal with the right mode.
//
// apiFetch is mocked (test-file-relative path, matching api.test.js's Toast mock)
// so clicks don't hit the network; we assert on the mock's call args.
import { describe, it, expect, beforeEach, vi } from 'vitest'
import { html } from 'htm/preact'
import { render, cleanup, fireEvent } from '@testing-library/preact'

vi.mock('../../../internal/web/static/app/api.js', () => ({
  apiFetch: vi.fn(() => Promise.resolve({})),
}))

const sidebarPath = '../../../internal/web/static/app/Sidebar.js'
const statePath = '../../../internal/web/static/app/state.js'
const uiStatePath = '../../../internal/web/static/app/uiState.js'
const apiPath = '../../../internal/web/static/app/api.js'

// --- fixtures ---------------------------------------------------------------

// One group, a top-level session, and a sub-session (level 1) under it.
const NESTED_ITEMS = [
  {
    type: 'group', level: 0,
    group: { path: 'work', name: 'Work', expanded: true, sessionCount: 2, order: 1 },
  },
  {
    type: 'session', level: 0, isSubSession: false,
    session: {
      id: 'sess-top', title: 'Top Session', tool: 'claude', groupPath: 'work',
      projectPath: '/home/user/project', status: 'running',
      parentSessionId: '', archived: false, autoName: false, taskDescription: '',
    },
  },
  {
    type: 'session', level: 1, isSubSession: true,
    session: {
      id: 'sess-sub', title: 'sub-handle', tool: 'claude', groupPath: 'work',
      projectPath: '/home/user/project', status: 'idle',
      parentSessionId: 'sess-top', archived: false,
      autoName: true, taskDescription: 'Refactoring auth',
    },
  },
]

// Group L0 -> subgroup L1 (with a session L2) for recursive collapse.
const SUBGROUP_ITEMS = [
  {
    type: 'group', level: 0,
    group: { path: 'work', name: 'Work', expanded: true, sessionCount: 1, order: 1 },
  },
  {
    type: 'group', level: 1,
    group: { path: 'work/api', name: 'API', expanded: true, sessionCount: 1, order: 2 },
  },
  {
    type: 'session', level: 2, isSubSession: false,
    session: {
      id: 'sess-deep', title: 'Deep Session', tool: 'claude', groupPath: 'work/api',
      projectPath: '/p', status: 'running', archived: false, autoName: false,
    },
  },
]

// Two sibling groups, each with a level-1 session. Collapsing the FIRST must
// hide only its own subtree; the sibling (and its session) must re-show. This
// pins the collapse walk's strict `it.level > collapseLevel` (a regression to
// `>=` would wrongly keep skipping the sibling group at the same level).
const SIBLING_ITEMS = [
  {
    type: 'group', level: 0,
    group: { path: 'a', name: 'A', expanded: true, sessionCount: 1, order: 1 },
  },
  {
    type: 'session', level: 1, isSubSession: true,
    session: { id: 'sess-a', title: 'Session A', tool: 'claude', groupPath: 'a', projectPath: '/p', status: 'running', archived: false, autoName: false },
  },
  {
    type: 'group', level: 0,
    group: { path: 'b', name: 'B', expanded: true, sessionCount: 1, order: 2 },
  },
  {
    type: 'session', level: 1, isSubSession: true,
    session: { id: 'sess-b', title: 'Session B', tool: 'claude', groupPath: 'b', projectPath: '/p', status: 'running', archived: false, autoName: false },
  },
]

// A group flagged collapsed-by-default (expanded:false) — used to verify the
// one-shot seed of the collapse Set, including the late-data (post-mount) path.
const COLLAPSED_DEFAULT_ITEMS = [
  {
    type: 'group', level: 0,
    group: { path: 'work', name: 'Work', expanded: false, sessionCount: 1, order: 1 },
  },
  {
    type: 'session', level: 1, isSubSession: true,
    session: { id: 'sess-x', title: 'Session X', tool: 'claude', groupPath: 'work', projectPath: '/p', status: 'idle', archived: false, autoName: false },
  },
]

// A group with one live session and one archived session.
const ARCHIVE_ITEMS = [
  {
    type: 'group', level: 0,
    group: { path: 'work', name: 'Work', expanded: true, sessionCount: 2, order: 1 },
  },
  {
    type: 'session', level: 0, isSubSession: false,
    session: {
      id: 'sess-live', title: 'Live Session', tool: 'claude', groupPath: 'work',
      projectPath: '/p', status: 'running', archived: false, autoName: false,
    },
  },
  {
    type: 'session', level: 0, isSubSession: false,
    session: {
      id: 'sess-arch', title: 'Archived Session', tool: 'claude', groupPath: 'work',
      projectPath: '/p', status: 'idle', archived: true, autoName: false,
    },
  },
]

async function seed(items, { showArchived = false } = {}) {
  const state = await import(statePath)
  const ui = await import(uiStatePath)
  state.sessionsSignal.value = items
  state.sessionCostsSignal.value = {}
  state.selectedIdSignal.value = null
  state.mutationsEnabledSignal.value = true
  state.editSessionDialogSignal.value = null
  state.groupNameDialogSignal.value = null
  state.createSessionDialogSignal.value = false
  ui.statusFiltersSignal.value = []
  ui.showArchivedSignal.value = showArchived
}

beforeEach(async () => {
  cleanup()
  const api = await import(apiPath)
  api.apiFetch.mockClear()
})

// --- tests ------------------------------------------------------------------

describe('Sidebar nesting', () => {
  it('indents rows by level: group L0, session L0, sub-session L1', async () => {
    await seed(NESTED_ITEMS)
    const { Sidebar } = await import(sidebarPath)
    const { container } = render(html`<${Sidebar}/>`)

    const group = container.querySelector('[data-group-path="work"]')
    const top = container.querySelector('[data-session-id="sess-top"]')
    const sub = container.querySelector('[data-session-id="sess-sub"]')

    expect(group).toBeTruthy()
    expect(top).toBeTruthy()
    expect(sub).toBeTruthy()

    expect(group.getAttribute('data-level')).toBe('0')
    expect(top.getAttribute('data-level')).toBe('0')
    expect(sub.getAttribute('data-level')).toBe('1')

    // The L1 sub-session indents more than the L0 session.
    const topPad = parseInt(top.style.paddingLeft, 10)
    const subPad = parseInt(sub.style.paddingLeft, 10)
    expect(subPad).toBeGreaterThan(topPad)

    // Nested row carries a guide border-left; the L0 row does not.
    expect(sub.style.borderLeft).toContain('var(--border)')
    expect(top.style.borderLeft).toBe('')
  })
})

describe('Sidebar recursive collapse', () => {
  it('collapsing a parent group hides a nested subgroup AND its session', async () => {
    await seed(SUBGROUP_ITEMS)
    const { Sidebar } = await import(sidebarPath)
    const { container } = render(html`<${Sidebar}/>`)

    // Before collapse: subgroup + deep session both present.
    expect(container.querySelector('[data-group-path="work/api"]')).toBeTruthy()
    expect(container.querySelector('[data-session-id="sess-deep"]')).toBeTruthy()

    // Click the parent group chevron (the whole header toggles).
    const parent = container.querySelector('[data-group-path="work"]')
    fireEvent.click(parent)

    // After collapse: the nested subgroup AND its session are gone (recursive).
    expect(container.querySelector('[data-group-path="work/api"]')).toBeFalsy()
    expect(container.querySelector('[data-session-id="sess-deep"]')).toBeFalsy()
    // The collapsed parent itself remains visible.
    expect(container.querySelector('[data-group-path="work"]')).toBeTruthy()
  })

  it('collapsing one group hides only its subtree; a same-level sibling re-shows', async () => {
    await seed(SIBLING_ITEMS)
    const { Sidebar } = await import(sidebarPath)
    const { container } = render(html`<${Sidebar}/>`)

    // All four rows present initially.
    expect(container.querySelector('[data-session-id="sess-a"]')).toBeTruthy()
    expect(container.querySelector('[data-group-path="b"]')).toBeTruthy()
    expect(container.querySelector('[data-session-id="sess-b"]')).toBeTruthy()

    // Collapse group A only.
    fireEvent.click(container.querySelector('[data-group-path="a"]'))

    // A's session is hidden...
    expect(container.querySelector('[data-session-id="sess-a"]')).toBeFalsy()
    // ...but the sibling group B AND its session re-show (strict `>` walk).
    expect(container.querySelector('[data-group-path="b"]')).toBeTruthy()
    expect(container.querySelector('[data-session-id="sess-b"]')).toBeTruthy()
  })
})

describe('Sidebar collapse seeding from group expanded state', () => {
  it('renders an expanded:false group collapsed on first load (data present at mount)', async () => {
    await seed(COLLAPSED_DEFAULT_ITEMS)
    const { Sidebar } = await import(sidebarPath)
    const { container } = render(html`<${Sidebar}/>`)

    // Group is present but its level-1 session is hidden (seeded collapsed).
    expect(container.querySelector('[data-group-path="work"]')).toBeTruthy()
    expect(container.querySelector('[data-session-id="sess-x"]')).toBeFalsy()
  })

  it('seeds collapse when menu data arrives AFTER mount (late-data path)', async () => {
    // Mount with EMPTY data first (mirrors main.js mounting before loadMenu()).
    const state = await import(statePath)
    const ui = await import(uiStatePath)
    state.sessionsSignal.value = []
    state.sessionCostsSignal.value = {}
    state.mutationsEnabledSignal.value = true
    ui.statusFiltersSignal.value = []
    ui.showArchivedSignal.value = false

    const { Sidebar } = await import(sidebarPath)
    const { container, rerender } = render(html`<${Sidebar}/>`)

    // Nothing yet.
    expect(container.querySelector('[data-group-path="work"]')).toBeFalsy()

    // Snapshot lands: an expanded:false group. The signal change re-renders.
    state.sessionsSignal.value = COLLAPSED_DEFAULT_ITEMS
    rerender(html`<${Sidebar}/>`)

    // The one-shot effect seeds the collapse Set from expanded:false → the
    // group renders collapsed (its session stays hidden) without manual toggle.
    expect(container.querySelector('[data-group-path="work"]')).toBeTruthy()
    expect(container.querySelector('[data-session-id="sess-x"]')).toBeFalsy()
  })
})

describe('Sidebar archived filter', () => {
  it('hides archived sessions when showArchived=false', async () => {
    await seed(ARCHIVE_ITEMS, { showArchived: false })
    const { Sidebar } = await import(sidebarPath)
    const { container } = render(html`<${Sidebar}/>`)

    expect(container.querySelector('[data-session-id="sess-live"]')).toBeTruthy()
    expect(container.querySelector('[data-session-id="sess-arch"]')).toBeFalsy()
  })

  it('shows archived sessions dimmed + badged when showArchived=true', async () => {
    await seed(ARCHIVE_ITEMS, { showArchived: true })
    const { Sidebar } = await import(sidebarPath)
    const { container } = render(html`<${Sidebar}/>`)

    const arch = container.querySelector('[data-session-id="sess-arch"]')
    expect(arch).toBeTruthy()
    expect(arch.getAttribute('data-archived')).toBe('true')
    // Dimmed.
    expect(arch.style.opacity).toBe('0.5')
    // Carries an [archived] badge.
    expect(arch.querySelector('[data-archived-badge="true"]')).toBeTruthy()
  })
})

describe('Sidebar displaySessionTitle', () => {
  it('renders the task description for an autoName session, not the handle', async () => {
    await seed(NESTED_ITEMS)
    const { Sidebar } = await import(sidebarPath)
    const { container } = render(html`<${Sidebar}/>`)

    const sub = container.querySelector('[data-session-id="sess-sub"]')
    const label = sub.querySelector('.tt')
    expect(label.textContent).toContain('Refactoring auth')
    expect(label.textContent).not.toContain('sub-handle')
  })
})

describe('Sidebar action wiring', () => {
  it('clicking Archive fires POST /api/sessions/{id}/archive', async () => {
    await seed(NESTED_ITEMS)
    const { Sidebar } = await import(sidebarPath)
    const { apiFetch } = await import(apiPath)
    const { container } = render(html`<${Sidebar}/>`)

    // Open the row kebab for the top session.
    const top = container.querySelector('[data-session-id="sess-top"]')
    fireEvent.click(top.querySelector('[data-kebab="true"]'))

    // Click Archive in the now-open row menu.
    const archiveItem = container.querySelector('[data-row-menu="true"] [data-act="archive"]')
    expect(archiveItem).toBeTruthy()
    fireEvent.click(archiveItem)

    expect(apiFetch).toHaveBeenCalledWith('POST', '/api/sessions/sess-top/archive')
  })

  it('clicking Edit sets editSessionDialogSignal to { sessionId }', async () => {
    await seed(NESTED_ITEMS)
    const { Sidebar } = await import(sidebarPath)
    const state = await import(statePath)
    const { container } = render(html`<${Sidebar}/>`)

    const top = container.querySelector('[data-session-id="sess-top"]')
    fireEvent.click(top.querySelector('[data-kebab="true"]'))
    const editItem = container.querySelector('[data-row-menu="true"] [data-act="edit"]')
    expect(editItem).toBeTruthy()
    fireEvent.click(editItem)

    // The dialog is opened by id; EditSessionDialog looks the session up in
    // menuModelSignal (see internal/web/static/app/EditSessionDialog.js).
    expect(state.editSessionDialogSignal.value).toBeTruthy()
    expect(state.editSessionDialogSignal.value.sessionId).toBe('sess-top')
  })

  it('a group affordance sets groupNameDialogSignal with the right mode (rename)', async () => {
    await seed(NESTED_ITEMS)
    const { Sidebar } = await import(sidebarPath)
    const state = await import(statePath)
    const { container } = render(html`<${Sidebar}/>`)

    // Open the group kebab.
    const group = container.querySelector('[data-group-path="work"]')
    fireEvent.click(group.querySelector('[data-group-kebab="true"]'))

    const renameItem = container.querySelector('[data-group-menu="true"] [data-act="rename"]')
    expect(renameItem).toBeTruthy()
    fireEvent.click(renameItem)

    const dlg = state.groupNameDialogSignal.value
    expect(dlg).toBeTruthy()
    expect(dlg.mode).toBe('rename')
    expect(dlg.groupPath).toBe('work')
  })

  it('New subgroup affordance sets groupNameDialogSignal with parentPath', async () => {
    await seed(NESTED_ITEMS)
    const { Sidebar } = await import(sidebarPath)
    const state = await import(statePath)
    const { container } = render(html`<${Sidebar}/>`)

    const group = container.querySelector('[data-group-path="work"]')
    fireEvent.click(group.querySelector('[data-group-kebab="true"]'))
    const subItem = container.querySelector('[data-group-menu="true"] [data-act="new-subgroup"]')
    fireEvent.click(subItem)

    const dlg = state.groupNameDialogSignal.value
    expect(dlg.mode).toBe('create')
    expect(dlg.parentPath).toBe('work')
  })

  it('does not render mutating menu items when mutations are disabled', async () => {
    await seed(NESTED_ITEMS)
    const state = await import(statePath)
    state.mutationsEnabledSignal.value = false
    const { Sidebar } = await import(sidebarPath)
    const { container } = render(html`<${Sidebar}/>`)

    const top = container.querySelector('[data-session-id="sess-top"]')
    fireEvent.click(top.querySelector('[data-kebab="true"]'))

    // Copy info is non-mutating and still present; Archive/Edit are gated out.
    expect(container.querySelector('[data-row-menu="true"] [data-act="copy"]')).toBeTruthy()
    expect(container.querySelector('[data-row-menu="true"] [data-act="archive"]')).toBeFalsy()
    expect(container.querySelector('[data-row-menu="true"] [data-act="edit"]')).toBeFalsy()
  })
})
