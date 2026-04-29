// e2e/parity-actions.spec.js -- one test per parity-matrix row that has a
// web counterpart. Drives the web HTTP API directly (Playwright's `request`
// fixture) and asserts the resulting state via the same /__fixture/snapshot
// endpoint that any TUI-side observer would query.
//
// "Both views see the same truth" is the contract — these tests fail if
// the web mutation lands but the snapshot the TUI reads doesn't reflect
// it, or vice versa.

import { test, expect } from '@playwright/test'

test.describe.configure({ mode: 'serial' })

async function resetFixture(request) {
  const res = await request.post('/__fixture/reset')
  expect(res.status()).toBe(204)
}

async function snapshot(request) {
  const res = await request.get('/__fixture/snapshot')
  expect(res.ok()).toBe(true)
  return res.json()
}

function findSession(snap, predicate) {
  for (const item of snap.items || []) {
    if (item.type === 'session' && item.session && predicate(item.session)) {
      return item.session
    }
  }
  return null
}

test.describe('parity: session lifecycle', () => {
  test.beforeEach(async ({ request }) => {
    await resetFixture(request)
  })

  test('create session — web POST mirrors TUI New action', async ({ request }) => {
    const before = await snapshot(request)
    const beforeCount = before.totalSessions

    const res = await request.post('/api/sessions', {
      data: {
        title: 'parity-create',
        tool: 'claude',
        projectPath: '/srv/parity-create',
        groupPath: 'work',
      },
    })
    expect(res.status()).toBe(201)
    const body = await res.json()
    expect(body.sessionId).toMatch(/^sess-/)

    const after = await snapshot(request)
    expect(after.totalSessions).toBe(beforeCount + 1)
    const created = findSession(after, (s) => s.id === body.sessionId)
    expect(created).not.toBeNull()
    expect(created.title).toBe('parity-create')
    expect(created.tool).toBe('claude')
    expect(created.groupPath).toBe('work')
    expect(created.projectPath).toBe('/srv/parity-create')
  })

  test('start session — web POST sets status to running', async ({ request }) => {
    const res = await request.post('/api/sessions/sess-001/start')
    expect(res.ok()).toBe(true)
    const after = await snapshot(request)
    const sess = findSession(after, (s) => s.id === 'sess-001')
    expect(sess.status).toBe('running')
  })

  test('stop session — web POST sets status to stopped', async ({ request }) => {
    const res = await request.post('/api/sessions/sess-002/stop')
    expect(res.ok()).toBe(true)
    const after = await snapshot(request)
    const sess = findSession(after, (s) => s.id === 'sess-002')
    expect(sess.status).toBe('stopped')
  })

  test('restart session — status returns to running', async ({ request }) => {
    await request.post('/api/sessions/sess-001/stop')
    const res = await request.post('/api/sessions/sess-001/restart')
    expect(res.ok()).toBe(true)
    const after = await snapshot(request)
    expect(findSession(after, (s) => s.id === 'sess-001').status).toBe('running')
  })

  test('fork session — web POST creates child with parent reference', async ({ request }) => {
    const res = await request.post('/api/sessions/sess-001/fork')
    expect(res.status()).toBe(200)
    const body = await res.json()
    expect(body.sessionId).toMatch(/^sess-/)
    expect(body.sessionId).not.toBe('sess-001')

    const after = await snapshot(request)
    const child = findSession(after, (s) => s.id === body.sessionId)
    expect(child).not.toBeNull()
    expect(child.parentSessionId).toBe('sess-001')
  })

  test('delete session — web DELETE removes from snapshot', async ({ request }) => {
    const res = await request.delete('/api/sessions/sess-004')
    expect(res.ok()).toBe(true)
    const after = await snapshot(request)
    expect(findSession(after, (s) => s.id === 'sess-004')).toBeNull()
  })

  test('unknown session action returns 404', async ({ request }) => {
    const res = await request.post('/api/sessions/sess-001/explode')
    expect(res.status()).toBe(404)
  })

  test('action on missing session returns 500 with error', async ({ request }) => {
    // The fixture mutator rejects unknown ids; web layer surfaces as 500.
    const res = await request.post('/api/sessions/does-not-exist/start')
    expect(res.status()).toBe(500)
  })
})

test.describe('parity: sync invariant — TUI-style change visible to web', () => {
  test.beforeEach(async ({ request }) => {
    await resetFixture(request)
  })

  test('status forced via /__fixture/session/{id}/status surfaces in /api/sessions', async ({
    request,
  }) => {
    // Simulate a TUI-side transition (something the web didn't initiate).
    const force = await request.post(
      '/__fixture/session/sess-001/status?to=waiting',
    )
    expect(force.status()).toBe(204)

    // The web's normal API now reflects it — that's the cross-layer contract.
    const res = await request.get('/api/sessions')
    expect(res.ok()).toBe(true)
    const body = await res.json()
    const sess = (body.sessions || []).find((s) => s.id === 'sess-001')
    expect(sess).toBeDefined()
    expect(sess.status).toBe('waiting')
  })
})

test.describe('parity: MISSING actions stay MISSING (regression guard)', () => {
  // The PARITY_MATRIX flags 30 actions the web does not yet expose. This
  // test pins the closure of that gap: when PR-B (or later) adds a real
  // endpoint for one of these, this test will fail loudly and the matrix
  // must be updated in lockstep.
  const expectedNotFound = [
    { method: 'POST', path: '/api/sessions/sess-001/close' },        // soft-close (`D` key)
    { method: 'POST', path: '/api/sessions/sess-001/rename' },       // rename (`r` key)
    { method: 'POST', path: '/api/sessions/sess-001/restart-fresh' },// restart fresh (`T` key)
    { method: 'POST', path: '/api/sessions/sess-001/mcps/exa' },     // attach MCP (`m` dialog)
    { method: 'DELETE', path: '/api/sessions/sess-001/mcps/exa' },   // detach MCP
    { method: 'POST', path: '/api/sessions/sess-001/skills/x' },     // attach skill (`s` dialog)
    { method: 'DELETE', path: '/api/sessions/sess-001/skills/x' },   // detach skill
    { method: 'POST', path: '/api/sessions/sess-001/notes' },        // edit notes (`e` key)
  ]

  for (const { method, path } of expectedNotFound) {
    test(`${method} ${path} is still 404 (not yet implemented)`, async ({ request }) => {
      const res = await request.fetch(path, { method, data: {} })
      // 404 OR 405 are both acceptable signals the route is unimplemented.
      expect([404, 405]).toContain(res.status())
    })
  }
})
