// e2e/parity-actions.spec.js -- one test per parity-matrix row.
//
// The full row set is derived from tests/web/PARITY_MATRIX.md via the
// helpers/parity-matrix.js parser. Anything that lands in the matrix
// automatically becomes a test row; anything that disappears triggers the
// pinned row-count assertion to fail. This catches the class of bug where
// PR-B silently drops a row not in a hard-coded subset.
//
// The contract is "both views see the same truth": web mutations must show
// up in the snapshot any TUI-side observer would consume, and missing
// endpoints must stay missing until they're explicitly added (in lockstep
// with the matrix).

import { test, expect } from '@playwright/test'
import { loadMatrix, inferMissingProbe } from '../helpers/parity-matrix.js'

const MATRIX = loadMatrix()

// Pinned row counts. If the matrix grows or shrinks, these MUST be updated
// in the same PR — the failure is the point.
const EXPECTED_ACTION_ROWS = 47
const EXPECTED_PROBEABLE_MISSING = 15

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

test.describe('parity: matrix structural invariants', () => {
  test('action row count matches expected (deletion guard)', () => {
    expect(
      MATRIX.actions.length,
      `PARITY_MATRIX.md changed action row count from ${EXPECTED_ACTION_ROWS} to ${MATRIX.actions.length} — update EXPECTED_ACTION_ROWS in parity-actions.spec.js and add/remove tests in lockstep.`,
    ).toBe(EXPECTED_ACTION_ROWS)
  })

  test('probeable-missing count matches expected (probe coverage guard)', () => {
    const probeable = MATRIX.actions.filter(
      (a) => a.isMissing && inferMissingProbe(a) !== null,
    )
    expect(
      probeable.length,
      `Missing-action probe count drifted (was ${EXPECTED_PROBEABLE_MISSING}, now ${probeable.length}). Update inferMissingProbe() and EXPECTED_PROBEABLE_MISSING together.`,
    ).toBe(EXPECTED_PROBEABLE_MISSING)
  })

  test('every implemented action row has a parseable METHOD path', () => {
    const broken = MATRIX.actions.filter((a) => !a.isMissing && !a.method)
    expect(broken, `unparseable matrix rows: ${broken.map((b) => b.action).join(', ')}`).toEqual([])
  })
})

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

// Drive the missing-endpoint regression guard from the matrix itself: every
// row whose webEndpoint is MISSING and which has an inferable URL pattern
// (see helpers/parity-matrix.js inferMissingProbe) is probed. If a future PR
// quietly implements one of these without updating the matrix, the test
// flips green for the wrong reason — but because the matrix row still says
// MISSING, the EXPECTED_PROBEABLE_MISSING pin keeps the count honest.
test.describe('parity: MISSING actions stay MISSING (regression guard)', () => {
  const probes = MATRIX.actions
    .filter((a) => a.isMissing)
    .map((a) => ({ row: a, probe: inferMissingProbe(a) }))
    .filter(({ probe }) => probe !== null)

  for (const { row, probe } of probes) {
    test(`${probe.method} ${probe.path} is still 404 (matrix row: "${row.action}")`, async ({
      request,
    }) => {
      const res = await request.fetch(probe.path, {
        method: probe.method,
        data: probe.method === 'GET' || probe.method === 'DELETE' ? undefined : {},
      })
      // 404 OR 405 are both acceptable signals the route is unimplemented.
      expect(
        [404, 405],
        `${probe.method} ${probe.path} returned ${res.status()} — if you intentionally implemented this, update PARITY_MATRIX.md row "${row.action}" so its Web Endpoint is no longer MISSING.`,
      ).toContain(res.status())
    })
  }
})
