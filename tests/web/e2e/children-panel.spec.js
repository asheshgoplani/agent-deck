// e2e/children-panel.spec.js -- Children-panel parity coverage.
//
// Closes the "Conductor child topology not exposed via web API" stub in
// the right rail. The Go handler ships in internal/web/handlers_children.go
// with its own surface test (issue1125_children_web_test.go); this file
// covers the browser-facing surface per agent-deck-tdd-feature SKILL.md:
// happy / failure / boundary case against the live fixture web server.
//
// The fixture exposes no `is_conductor` flag, so we mark a session as a
// conductor by including the literal string "conductor" in its title —
// dataModel.js deriveKind() promotes it to kind === 'conductor', which is
// the UI gate for showing the CHILDREN card.

import { test, expect } from '@playwright/test'

// Seed a parent-child topology on top of the default fixture data:
//
//   conductor-e2e  (created via POST /api/sessions)
//     ├── conductor-e2e (fork)            ← direct child
//     └── conductor-e2e (fork)            ← direct child, in turn forked
//           └── conductor-e2e (fork) (fork) ← grandchild (deep nesting)
//
// Returns the session ids in an object so tests can address them.
async function seedConductorTree(request) {
  const reset = await request.post('/__fixture/reset')
  expect(reset.status()).toBe(204)

  const created = await request.post('/api/sessions', {
    data: {
      title: 'conductor-e2e',
      tool: 'claude',
      projectPath: '/srv/conductor-e2e',
      groupPath: 'work',
    },
  })
  expect(created.status()).toBe(201)
  const { sessionId: conductorID } = await created.json()

  const forkA = await request.post(`/api/sessions/${conductorID}/fork`)
  expect(forkA.status()).toBe(200)
  const { sessionId: childAID } = await forkA.json()

  const forkB = await request.post(`/api/sessions/${conductorID}/fork`)
  expect(forkB.status()).toBe(200)
  const { sessionId: childBID } = await forkB.json()

  const forkG = await request.post(`/api/sessions/${childBID}/fork`)
  expect(forkG.status()).toBe(200)
  const { sessionId: grandID } = await forkG.json()

  return { conductorID, childAID, childBID, grandID }
}

test.describe('children endpoint: API surface', () => {
  test.beforeEach(async ({ request }) => {
    await request.post('/__fixture/reset')
  })

  test('GET on an unknown session returns 404', async ({ request }) => {
    const res = await request.get('/api/sessions/does-not-exist/children')
    expect(res.status()).toBe(404)
  })

  test('GET on a session with no children returns 200 with empty tree (not 404)', async ({ request }) => {
    const res = await request.get('/api/sessions/sess-001/children')
    expect(res.status()).toBe(200)
    const body = await res.json()
    expect(body.sessionId).toBe('sess-001')
    expect(Array.isArray(body.children)).toBe(true)
    expect(body.children.length).toBe(0)
  })

  test('GET on a conductor returns nested tree with deep grandchildren', async ({ request }) => {
    const { conductorID, childAID, childBID, grandID } = await seedConductorTree(request)

    const res = await request.get(`/api/sessions/${conductorID}/children`)
    expect(res.status()).toBe(200)
    const body = await res.json()
    expect(body.sessionId).toBe(conductorID)
    expect(body.children).toHaveLength(2)

    const ids = body.children.map((c) => c.id).sort()
    expect(ids).toEqual([childAID, childBID].sort())

    // Grandchild must appear under its parent, not at the top level.
    const bNode = body.children.find((c) => c.id === childBID)
    expect(bNode).toBeDefined()
    expect(bNode.children).toHaveLength(1)
    expect(bNode.children[0].id).toBe(grandID)
    // Leaf nodes carry an empty array, never null/undefined.
    expect(bNode.children[0].children).toEqual([])
  })

  test('non-GET methods on /children return 405', async ({ request }) => {
    for (const method of ['post', 'put', 'delete']) {
      const res = await request.fetch('/api/sessions/sess-001/children', { method })
      expect(
        res.status(),
        `expected 405 for ${method.toUpperCase()}, got ${res.status()}`,
      ).toBe(405)
    }
  })
})

test.describe('children panel: UI rendering', () => {
  // desktop-only: the right-rail CHILDREN card is not rendered on the phone touch-first layout (<768px) — the right rail (and sidebar `.sess` rows) is collapsed.
  test.skip(({ viewport }) => (viewport?.width || 1280) < 768, 'phone viewport: children panel is desktop/tablet only')
  test.beforeEach(async ({ request }) => {
    await request.post('/__fixture/reset')
  })

  test('conductor session shows children tree in right rail', async ({ page, request }) => {
    const { conductorID, childAID, childBID } = await seedConductorTree(request)

    await page.goto(`/s/${conductorID}`)
    // Wait for the session list to populate (SSE handshake).
    await page.waitForSelector('.sess', { timeout: 5000 })
    // The right rail CHILDREN card only renders when session.kind === 'conductor'.
    // dataModel.js deriveKind() flips to 'conductor' when the title matches /conductor/i.
    const childrenCard = page.locator('.card', { hasText: 'CHILDREN' })
    await expect(childrenCard).toBeVisible({ timeout: 5000 })

    // Both forks must appear in the tree. Use data attributes the
    // ChildNode component emits so we are not coupled to titles or order.
    const tree = childrenCard.locator('.children-tree')
    await expect(tree).toBeVisible()
    await expect(tree.locator(`[data-session-id="${childAID}"]`)).toBeVisible()
    await expect(tree.locator(`[data-session-id="${childBID}"]`)).toBeVisible()
  })

  test('deep nesting renders grandchild beneath the right intermediate', async ({ page, request }) => {
    const { conductorID, childBID, grandID } = await seedConductorTree(request)

    await page.goto(`/s/${conductorID}`)
    await page.waitForSelector('.sess', { timeout: 5000 })

    const childrenCard = page.locator('.card', { hasText: 'CHILDREN' })
    await expect(childrenCard).toBeVisible({ timeout: 5000 })

    // Grandchild must live inside childB's subtree, not at the top level.
    const childBNode = childrenCard.locator(`[data-session-id="${childBID}"]`)
    const grandNode = childBNode.locator(`[data-session-id="${grandID}"]`)
    await expect(grandNode).toBeVisible()

    // Grandchild's data-depth must be greater than childB's (visual indent).
    const childBDepth = await childBNode.first().getAttribute('data-depth')
    const grandDepth = await grandNode.first().getAttribute('data-depth')
    expect(Number(grandDepth)).toBeGreaterThan(Number(childBDepth))
  })

  test('non-conductor session does NOT render the children card (UI gate)', async ({ page, request }) => {
    // sess-001 is a normal claude session — not promoted to conductor by
    // deriveKind, so the panel must not appear even though the toggle is
    // on by default. Visual baselines depend on this invariant.
    await request.post('/__fixture/reset')
    await page.goto('/s/sess-001')
    await page.waitForSelector('.sess', { timeout: 5000 })
    await page.waitForTimeout(200) // allow rail to settle

    const childrenCard = page.locator('.card', { hasText: 'CHILDREN' })
    await expect(childrenCard).toHaveCount(0)
  })

  test('live updates: new fork appears in the tree within ~1s', async ({ page, request }) => {
    const { conductorID, childAID } = await seedConductorTree(request)

    await page.goto(`/s/${conductorID}`)
    await page.waitForSelector('.sess', { timeout: 5000 })

    const childrenCard = page.locator('.card', { hasText: 'CHILDREN' })
    await expect(childrenCard).toBeVisible({ timeout: 5000 })
    const tree = childrenCard.locator('.children-tree')
    await expect(tree.locator(`[data-session-id="${childAID}"]`)).toBeVisible()

    // Create another fork — the SSE menu broadcast should re-render the
    // panel without a manual reload. The bound is generous (~1.5s) to
    // tolerate scheduler jitter on CI; <1s is achievable on a warm box.
    const fork = await request.post(`/api/sessions/${conductorID}/fork`)
    expect(fork.status()).toBe(200)
    const { sessionId: newChildID } = await fork.json()

    await expect(
      tree.locator(`[data-session-id="${newChildID}"]`),
    ).toBeVisible({ timeout: 1500 })
  })
})
