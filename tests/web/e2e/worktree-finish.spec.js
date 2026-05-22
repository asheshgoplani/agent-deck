// e2e/worktree-finish.spec.js — Web UI worktree finish coverage.
//
// Closes the "Finish worktree" MISSING row under "WORKTREE OPERATIONS"
// in tests/web/PARITY_MATRIX.md (issue #1126).
//
// Per ~/.agent-deck/skills/pool/agent-deck-tdd-feature/SKILL.md we cover
// happy path, failure mode (not a worktree session), and boundary
// (custom merge target via the `into` body field) against the live
// fixture server. The fixture's FinishWorktree is deterministic — it
// validates the worktree fields, removes the session, and returns the
// finish result without invoking real git.

import { test, expect } from '@playwright/test'

test.describe('worktree finish (#1126)', () => {
  test.beforeEach(async ({ request }) => {
    await request.post('/__fixture/reset')
  })

  test('POST /api/sessions/{id}/worktree/finish removes the session and reports the branch', async ({ request }) => {
    // sess-001 is seeded with worktree fields (see fixtureStore.seed()).
    const before = await request.get('/__fixture/snapshot')
    const beforeBody = await before.json()
    const beforeSess = beforeBody.items.find(i => i.session && i.session.id === 'sess-001')
    expect(beforeSess).toBeTruthy()
    expect(beforeSess.session.worktreeBranch).toBe('feat/fixture')

    // Finish with default options (merge into auto-detected default
    // branch, delete source branch).
    const res = await request.post('/api/sessions/sess-001/worktree/finish', { data: {} })
    expect(res.status()).toBe(200)
    const body = await res.json()
    expect(body.sessionId).toBe('sess-001')
    expect(body.branch).toBe('feat/fixture')
    expect(body.merged).toBe(true)
    expect(body.branchDeleted).toBe(true)
    expect(body.mergedInto).toBeTruthy()

    // Session must be gone from the snapshot.
    const after = await request.get('/__fixture/snapshot')
    const afterBody = await after.json()
    expect(afterBody.items.some(i => i.session && i.session.id === 'sess-001')).toBe(false)
  })

  test('POST /api/sessions/{id}/worktree/finish honors `into` and `keepBranch`', async ({ request }) => {
    // Boundary case: explicit target branch + keep the source branch.
    const res = await request.post('/api/sessions/sess-001/worktree/finish', {
      data: { into: 'develop', keepBranch: true },
    })
    expect(res.status()).toBe(200)
    const body = await res.json()
    expect(body.mergedInto).toBe('develop')
    expect(body.branchDeleted).toBe(false)
  })

  test('POST /api/sessions/{id}/worktree/finish 400s when session is not in a worktree', async ({ request }) => {
    // sess-002 has no worktree fields populated.
    const res = await request.post('/api/sessions/sess-002/worktree/finish', { data: {} })
    expect(res.status()).toBe(400)
    const body = await res.json()
    expect(body.error.code).toBe('INVALID_REQUEST')
  })

  test('POST /api/sessions/{id}/worktree/finish 404s for unknown session id', async ({ request }) => {
    const res = await request.post('/api/sessions/sess-does-not-exist/worktree/finish', { data: {} })
    expect(res.status()).toBe(404)
    const body = await res.json()
    expect(body.error.code).toBe('NOT_FOUND')
  })
})
