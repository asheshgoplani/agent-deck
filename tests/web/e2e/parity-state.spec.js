// e2e/parity-state.spec.js -- field-level parity assertions.
//
// Every state field listed in PARITY_MATRIX.md must be retrievable from the
// web JSON. This test pins the current set so that:
//   - if the web JSON drops a field that the TUI still shows, the test fails
//   - if a NEW field is added (e.g. `notes` exposed), this test plus the
//     matrix must be updated in lockstep — the failure is a useful nudge.

import { test, expect } from '@playwright/test'

test.describe('parity: state fields surfaced by /api/sessions', () => {
  test('present fields match the matrix-documented set', async ({ request }) => {
    await request.post('/__fixture/reset')
    const res = await request.get('/api/sessions')
    expect(res.ok()).toBe(true)
    const body = await res.json()
    expect(body.sessions.length).toBeGreaterThan(0)

    // Pick a representative session.
    const s = body.sessions.find((x) => x.id === 'sess-001')
    expect(s).toBeDefined()

    const expectedKeys = [
      'id', 'title', 'tool', 'status', 'groupPath', 'projectPath',
      'order', 'createdAt',
    ].sort()
    const actualKeys = Object.keys(s).filter((k) => s[k] !== undefined && s[k] !== null && s[k] !== '').sort()
    // Every expected key MUST be present.
    for (const k of expectedKeys) {
      expect(actualKeys, `expected key ${k} on session JSON`).toContain(k)
    }
  })

  test('matrix-documented MISSING fields stay absent until intentionally added', async ({
    request,
  }) => {
    await request.post('/__fixture/reset')
    const res = await request.get('/api/sessions')
    const body = await res.json()
    const s = body.sessions.find((x) => x.id === 'sess-001')
    expect(s).toBeDefined()

    // A subset of fields the matrix flags as MISSING. If any of these
    // appears, the matrix is out of date AND the parity tests for that
    // field need to be added in the same PR.
    const stillMissing = [
      'notes', 'color', 'command', 'wrapper', 'channels', 'extraArgs',
      'toolOptions', 'loadedMcpNames', 'sandbox', 'sshHost', 'worktreePath',
    ]
    for (const k of stillMissing) {
      expect(s[k], `field ${k} surfaced unexpectedly — update PARITY_MATRIX.md`).toBeUndefined()
    }
  })
})
