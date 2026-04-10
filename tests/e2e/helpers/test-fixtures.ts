import { Page } from '@playwright/test'

// ---------------------------------------------------------------------------
// Fixture data constants
// ---------------------------------------------------------------------------

export const FIXTURE_MENU = {
  items: [
    {
      type: 'group', level: 0, index: 0,
      group: { path: 'work', name: 'Work', expanded: true, sessionCount: 2, order: 0 },
    },
    {
      type: 'session', level: 1, index: 1,
      session: {
        id: 'sess-001', title: 'Build pipeline', tool: 'claude', status: 'running',
        groupPath: 'work', projectPath: '/home/user/project-a', order: 0,
        createdAt: '2026-04-01T10:00:00Z',
      },
    },
    {
      type: 'session', level: 1, index: 2,
      session: {
        id: 'sess-002', title: 'Research docs', tool: 'shell', status: 'waiting',
        groupPath: 'work', projectPath: '/home/user/project-b', order: 1,
        createdAt: '2026-04-01T11:00:00Z',
      },
    },
    {
      type: 'group', level: 0, index: 3,
      group: { path: 'personal', name: 'Personal', expanded: true, sessionCount: 2, order: 1 },
    },
    {
      type: 'session', level: 1, index: 4,
      session: {
        id: 'sess-003', title: 'Blog drafts', tool: 'claude', status: 'idle',
        groupPath: 'personal', projectPath: '/home/user/blog', order: 0,
        createdAt: '2026-04-02T09:00:00Z',
      },
    },
    {
      type: 'session', level: 1, index: 5,
      session: {
        id: 'sess-004', title: 'Errored task', tool: 'shell', status: 'error',
        groupPath: 'personal', projectPath: '/home/user/scripts', order: 1,
        createdAt: '2026-04-02T10:00:00Z',
      },
    },
  ],
}

export const FIXTURE_SETTINGS = {
  webMutations: true,
  profile: '_test',
  readOnly: false,
  version: '1.5.0',
}

export const FIXTURE_PROFILES = {
  current: '_test',
  profiles: ['_test', 'default', 'work'],
}

export const FIXTURE_COSTS_SUMMARY = {
  today_usd: 12.34, today_events: 5,
  week_usd: 67.89, week_events: 42,
  month_usd: 234.56, month_events: 200,
  projected_usd: 500.00,
}

export const FIXTURE_COSTS_DAILY = [
  { date: '2026-04-04', cost_usd: 5.01 },
  { date: '2026-04-05', cost_usd: 7.12 },
  { date: '2026-04-06', cost_usd: 9.44 },
  { date: '2026-04-07', cost_usd: 3.33 },
  { date: '2026-04-08', cost_usd: 6.78 },
  { date: '2026-04-09', cost_usd: 8.01 },
  { date: '2026-04-10', cost_usd: 12.34 },
]

export const FIXTURE_COSTS_MODELS = {
  'claude-opus-4': 120.5,
  'claude-sonnet-4': 84.2,
  'gpt-4o': 30.0,
}

// ---------------------------------------------------------------------------
// ID generation
// ---------------------------------------------------------------------------

let idCounter = 100
export function generateSessionId(): string {
  idCounter++
  return `sess-${String(idCounter).padStart(3, '0')}`
}

export function resetIdCounter(): void {
  idCounter = 100
}

// ---------------------------------------------------------------------------
// Mock all read-only endpoints
// ---------------------------------------------------------------------------

interface MockOverrides {
  menu?: any
  settings?: any
  profiles?: any
  costsSummary?: any
  costsDaily?: any
  costsModels?: any
}

export async function mockAllEndpoints(page: Page, overrides: MockOverrides = {}): Promise<void> {
  const menu = overrides.menu || FIXTURE_MENU
  await page.route('**/api/menu*', r => r.fulfill({ json: menu }))
  await page.route('**/api/settings*', r => r.fulfill({ json: overrides.settings || FIXTURE_SETTINGS }))
  await page.route('**/api/profiles*', r => r.fulfill({ json: overrides.profiles || FIXTURE_PROFILES }))
  await page.route('**/api/costs/summary*', r => r.fulfill({ json: overrides.costsSummary || FIXTURE_COSTS_SUMMARY }))
  await page.route('**/api/costs/daily*', r => r.fulfill({ json: overrides.costsDaily || FIXTURE_COSTS_DAILY }))
  await page.route('**/api/costs/models*', r => r.fulfill({ json: overrides.costsModels || FIXTURE_COSTS_MODELS }))
  await page.route('**/api/costs/batch*', r => r.fulfill({ json: { costs: {} } }))
  // SSE stream keeps the connection open indefinitely; abort it so
  // waitForLoadState('domcontentloaded') and header/ready probes settle.
  await page.route('**/events/menu*', r => r.abort())
}

// ---------------------------------------------------------------------------
// Mutable state for session CRUD lifecycle testing
// ---------------------------------------------------------------------------

export interface TestState {
  menuItems: any[]
  sessions: Record<string, any>
  groups: Record<string, any>
}

export function createTestState(baseMenu?: any): TestState {
  const menu = baseMenu || FIXTURE_MENU
  const state: TestState = {
    menuItems: JSON.parse(JSON.stringify(menu.items)),
    sessions: {},
    groups: {},
  }
  // Index existing sessions and groups from the menu
  for (const item of state.menuItems) {
    if (item.type === 'session' && item.session) {
      state.sessions[item.session.id] = item.session
    }
    if (item.type === 'group' && item.group) {
      state.groups[item.group.path] = item.group
    }
  }
  return state
}

function rebuildMenu(state: TestState): any {
  return { items: state.menuItems }
}

export async function mockSessionCRUD(page: Page, state: TestState): Promise<void> {
  // Intercept POST /api/sessions (collection endpoint for create)
  // and POST/DELETE /api/sessions/{id}[/{action}] (per-session actions).
  // We use a single broad route and dispatch based on method + path.
  await page.route('**/api/sessions', async (route) => {
    const request = route.request()
    if (request.method() === 'POST') {
      // Create session
      const body = request.postDataJSON()
      const newId = generateSessionId()
      const newSession = {
        id: newId,
        title: body.title || 'Untitled',
        tool: body.tool || 'claude',
        status: 'running',
        groupPath: body.groupPath || '',
        projectPath: body.projectPath || '/tmp',
        order: Object.keys(state.sessions).length,
        createdAt: new Date().toISOString(),
      }
      state.sessions[newId] = newSession
      // Find the group to insert after, or append at end
      const groupPath = newSession.groupPath
      let insertIdx = state.menuItems.length
      if (groupPath) {
        // Find last item belonging to this group
        for (let i = state.menuItems.length - 1; i >= 0; i--) {
          const item = state.menuItems[i]
          if (item.type === 'group' && item.group && item.group.path === groupPath) {
            insertIdx = i + 1
            break
          }
          if (item.type === 'session' && item.session && item.session.groupPath === groupPath) {
            insertIdx = i + 1
            break
          }
        }
      }
      const level = groupPath ? 1 : 0
      state.menuItems.splice(insertIdx, 0, {
        type: 'session', level, index: insertIdx,
        session: newSession,
      })
      // Update the menu route to reflect the new state
      await page.route('**/api/menu*', r => r.fulfill({ json: rebuildMenu(state) }))
      return route.fulfill({ status: 201, json: { sessionId: newId } })
    }
    return route.fallback()
  })

  // Per-session action routes: /api/sessions/{id} and /api/sessions/{id}/{action}
  await page.route('**/api/sessions/**', async (route) => {
    const request = route.request()
    const url = new URL(request.url())
    const pathParts = url.pathname.replace(/^\/api\/sessions\//, '').split('/')
    const sessionId = decodeURIComponent(pathParts[0])
    const action = pathParts[1] || ''

    if (request.method() === 'DELETE' && !action) {
      // Delete session
      delete state.sessions[sessionId]
      state.menuItems = state.menuItems.filter(
        item => !(item.type === 'session' && item.session && item.session.id === sessionId)
      )
      // Update group session counts
      for (const item of state.menuItems) {
        if (item.type === 'group' && item.group) {
          item.group.sessionCount = state.menuItems.filter(
            mi => mi.type === 'session' && mi.session && mi.session.groupPath === item.group.path
          ).length
        }
      }
      await page.route('**/api/menu*', r => r.fulfill({ json: rebuildMenu(state) }))
      return route.fulfill({ json: { deleted: sessionId } })
    }

    if (request.method() === 'POST') {
      const session = state.sessions[sessionId]
      if (!session) {
        return route.fulfill({ status: 404, json: { error: { message: 'session not found' } } })
      }
      switch (action) {
        case 'stop':
          session.status = 'stopped'
          await page.route('**/api/menu*', r => r.fulfill({ json: rebuildMenu(state) }))
          return route.fulfill({ json: { sessionId } })
        case 'start':
          session.status = 'running'
          await page.route('**/api/menu*', r => r.fulfill({ json: rebuildMenu(state) }))
          return route.fulfill({ json: { sessionId } })
        case 'restart':
          session.status = 'running'
          await page.route('**/api/menu*', r => r.fulfill({ json: rebuildMenu(state) }))
          return route.fulfill({ json: { sessionId } })
        case 'fork': {
          const forkId = generateSessionId()
          const forked = { ...session, id: forkId, title: session.title + ' (fork)', status: 'running' }
          state.sessions[forkId] = forked
          state.menuItems.push({ type: 'session', level: 1, index: state.menuItems.length, session: forked })
          await page.route('**/api/menu*', r => r.fulfill({ json: rebuildMenu(state) }))
          return route.fulfill({ json: { sessionId: forkId } })
        }
        default:
          return route.fulfill({ status: 404, json: { error: { message: 'unknown action' } } })
      }
    }

    return route.fallback()
  })
}

export async function mockGroupCRUD(page: Page, state: TestState): Promise<void> {
  // POST /api/groups (create)
  await page.route('**/api/groups', async (route) => {
    const request = route.request()
    if (request.method() === 'POST') {
      const body = request.postDataJSON()
      const groupPath = body.parentPath ? body.parentPath + '/' + body.name : body.name
      const newGroup = {
        name: body.name,
        path: groupPath,
        expanded: true,
        sessionCount: 0,
        order: Object.keys(state.groups).length,
      }
      state.groups[groupPath] = newGroup
      const level = body.parentPath ? 1 : 0
      state.menuItems.push({
        type: 'group', level, index: state.menuItems.length,
        group: newGroup,
      })
      await page.route('**/api/menu*', r => r.fulfill({ json: rebuildMenu(state) }))
      return route.fulfill({ status: 201, json: { path: groupPath } })
    }
    return route.fallback()
  })

  // PATCH /api/groups/{path} (rename) and DELETE /api/groups/{path} (delete)
  await page.route('**/api/groups/**', async (route) => {
    const request = route.request()
    const url = new URL(request.url())
    const groupPath = decodeURIComponent(url.pathname.replace(/^\/api\/groups\//, ''))

    if (request.method() === 'PATCH') {
      const body = request.postDataJSON()
      const group = state.groups[groupPath]
      if (group) {
        group.name = body.name
      }
      // Update in menuItems too
      for (const item of state.menuItems) {
        if (item.type === 'group' && item.group && item.group.path === groupPath) {
          item.group.name = body.name
        }
      }
      await page.route('**/api/menu*', r => r.fulfill({ json: rebuildMenu(state) }))
      return route.fulfill({ json: { path: groupPath, name: body.name } })
    }

    if (request.method() === 'DELETE') {
      delete state.groups[groupPath]
      state.menuItems = state.menuItems.filter(
        item => !(item.type === 'group' && item.group && item.group.path === groupPath)
      )
      // Move orphaned sessions to default group (matching backend behavior)
      for (const item of state.menuItems) {
        if (item.type === 'session' && item.session && item.session.groupPath === groupPath) {
          item.session.groupPath = ''
          item.level = 0
        }
      }
      await page.route('**/api/menu*', r => r.fulfill({ json: rebuildMenu(state) }))
      return route.fulfill({ json: { deleted: groupPath } })
    }

    return route.fallback()
  })
}

// ---------------------------------------------------------------------------
// Utility
// ---------------------------------------------------------------------------

export async function waitForAppReady(page: Page): Promise<void> {
  await page.waitForSelector('header', { state: 'attached', timeout: 15000 })
  await page.waitForTimeout(150)
}
