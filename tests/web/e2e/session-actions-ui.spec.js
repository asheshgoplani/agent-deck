// e2e/session-actions-ui.spec.js -- UI wiring of the sidebar row action
// buttons (Start / Stop / Restart / Fork / Delete / Worktree-finish).
//
// The REST endpoints themselves are covered by parity-actions.spec.js and
// worktree-finish.spec.js; close (Shift+D) is covered by close-undo.spec.js.
// THIS spec covers the click → API → SSE round-trip through the buttons that
// Sidebar.js SessionItem renders.
//
// Grounded in the fixture seed (tests/web/fixtures/cmd/web-fixture/main.go):
//   sess-001 "agent-deck"    status=idle    worktreeBranch=feat/fixture (worktree row)
//   sess-002 "frontend"      status=running
//   sess-003 "innotrade-api" status=idle
//   sess-004 "scratch"       status=idle
//
// Source audit notes (assertions pinned to these):
//   - Action buttons live in `.sess .actions` which is `display:none` until
//     `.sess:hover` (app.css), so every test hovers the row first.
//   - Status renders as `<span class="dot <status>">` (icons.js Dot); the
//     fixture transitions are start/restart→running, stop→stopped.
//   - Fork button only renders when `s.canFork` (Sidebar.js). The fixture
//     seed never sets CanFork, so no seeded row shows it; the canFork=true
//     path is exercised by rewriting /api/menu in-flight. The Sidebar POSTs
//     a `title: "<title>-fork"` body, but handlers_sessions.go ignores the
//     body entirely — the mutator names the child. The fixture's
//     ForkSession appends " (fork)", so the real child title is
//     "agent-deck (fork)" (NOT "-fork"); we assert the server-side truth.
//   - Delete + worktree-finish route through ConfirmDialog.js whose confirm
//     button is always labeled "Delete" and cancel is "Cancel".
//
// Phone (<768px) skips: sidebar rows are desktop/tablet-only (same pattern
// as keyboard-parity.spec.js / skills.spec.js).

import { test, expect } from '@playwright/test'

// The app registers a service worker (sw.js) whose fetch handler takes over
// page requests once active — that bypasses page.route(), which the
// canFork=true fork test relies on to doctor /api/menu. Block SWs for this
// file so route interception is deterministic (no behavior under test here
// depends on the SW).
test.use({ serviceWorkers: 'block' })

const SEEDED_COUNT = 4

function rowFor(page, title) {
  return page.locator('.sess', { has: page.locator('.tt', { hasText: title }) })
}

async function gotoSidebar(page) {
  await page.goto('/')
  await expect(page.locator('.sess')).toHaveCount(SEEDED_COUNT, { timeout: 5000 })
}

test.describe('sidebar session action buttons', () => {
  test.skip(({ viewport }) => (viewport?.width || 1280) < 768, 'phone viewport: sidebar action buttons are desktop/tablet only')

  test.beforeEach(async ({ request }) => {
    await request.post('/__fixture/reset')
  })

  test('Start button on an idle session flips the status pill to running (SSE round-trip)', async ({ page }) => {
    await gotoSidebar(page)
    const row = rowFor(page, 'scratch') // sess-004, seeded idle
    await expect(row.locator('.dot.idle')).toHaveCount(1)

    await row.hover() // actions bar is display:none until row hover
    await row.locator('[data-testid="session-start-btn"]').click()

    // Fixture StartSession → status=running; notifyMenuChanged pushes the
    // new snapshot over SSE and the Dot re-renders.
    await expect(row.locator('.dot.running')).toHaveCount(1, { timeout: 4000 })
    // The start/stop slot is status-driven: running rows render Stop.
    await expect(row.locator('[data-testid="session-stop-btn"]')).toHaveCount(1)
    await expect(row.locator('[data-testid="session-start-btn"]')).toHaveCount(0)
  })

  test('Stop button on the running session changes the status pill', async ({ page, request }) => {
    await gotoSidebar(page)
    const row = rowFor(page, 'frontend') // sess-002, the only seeded running session
    await expect(row.locator('.dot.running')).toHaveCount(1)

    await row.hover()
    await row.locator('[data-testid="session-stop-btn"]').click()

    // Fixture StopSession → status=stopped (parity-actions pins the API).
    await expect(row.locator('.dot.stopped')).toHaveCount(1, { timeout: 4000 })
    // Non-running/waiting rows render Start again.
    await expect(row.locator('[data-testid="session-start-btn"]')).toHaveCount(1)

    const snap = await (await request.get('/__fixture/snapshot')).json()
    const sess = snap.items.find(i => i.session && i.session.id === 'sess-002')
    expect(sess.session.status).toBe('stopped')
  })

  test('Restart button renders on every row and sets status to running', async ({ page }) => {
    await gotoSidebar(page)
    // Source: Restart is unconditional in SessionItem — one per session row.
    await expect(page.locator('[data-testid="session-restart-btn"]')).toHaveCount(SEEDED_COUNT)

    const row = rowFor(page, 'innotrade-api') // sess-003, seeded idle
    await row.hover()
    await row.locator('[data-testid="session-restart-btn"]').click()
    await expect(row.locator('.dot.running')).toHaveCount(1, { timeout: 4000 })
  })

  test('Fork button is hidden for seeded sessions (canFork=false)', async ({ page }) => {
    await gotoSidebar(page)
    // The fixture seed never sets CanFork, and Sidebar.js gates the button
    // on `s.canFork` — so no row carries it. (Buttons exist in the DOM even
    // un-hovered; only CSS hides them, so a count is meaningful.)
    await expect(page.locator('[data-testid="session-fork-btn"]')).toHaveCount(0)
  })

  test('Fork button (canFork=true) POSTs fork; child is titled "<title> (fork)" by the server', async ({ page, request }) => {
    // Rewrite the initial /api/menu payload so every session reports
    // canFork=true, and pin the SSE stream shut so the un-doctored snapshot
    // can't flip the button back off mid-test.
    await page.route('**/api/menu', async (route) => {
      const response = await route.fetch()
      const body = await response.json()
      for (const it of body.items || []) {
        if (it.session) it.session.canFork = true
      }
      await route.fulfill({ response, json: body })
    })
    await page.route('**/events/menu*', (route) => route.abort())

    await gotoSidebar(page)
    const row = rowFor(page, 'agent-deck') // sess-001
    await expect(row.locator('[data-testid="session-fork-btn"]')).toHaveCount(1)

    await row.hover()
    const forkResponse = page.waitForResponse(
      r => r.url().includes('/api/sessions/sess-001/fork') && r.request().method() === 'POST',
    )
    await row.locator('[data-testid="session-fork-btn"]').click()
    expect((await forkResponse).status()).toBe(200)

    // Server-side truth (SSE is blocked above, so assert via fixture
    // snapshot): handlers_sessions.go ignores the client-sent
    // "agent-deck-fork" title; the fixture mutator appends " (fork)".
    const snap = await (await request.get('/__fixture/snapshot')).json()
    const child = snap.items.find(i => i.session && i.session.parentSessionId === 'sess-001')
    expect(child).toBeTruthy()
    expect(child.session.title).toBe('agent-deck (fork)')
  })

  test('Delete button: ConfirmDialog message; Cancel keeps the row, Confirm removes it', async ({ page, request }) => {
    await gotoSidebar(page)
    const row = rowFor(page, 'scratch') // sess-004

    // Open the confirm dialog.
    await row.hover()
    await row.locator('[data-testid="session-delete-btn"]').click()
    const dialog = page.locator('[role="dialog"]')
    await expect(dialog).toBeVisible()
    // Exact copy from Sidebar.js doAction('delete').
    await expect(dialog).toContainText('Delete session "scratch"? This stops the tmux session and removes metadata.')

    // Cancel → no mutation: row still there, fixture untouched.
    await dialog.getByRole('button', { name: 'Cancel' }).click()
    await expect(dialog).toHaveCount(0)
    await expect(page.locator('.sess')).toHaveCount(SEEDED_COUNT)
    let snap = await (await request.get('/__fixture/snapshot')).json()
    expect(snap.items.some(i => i.session && i.session.id === 'sess-004')).toBe(true)

    // Re-open and confirm (ConfirmDialog's confirm button is labeled "Delete").
    await row.hover()
    await row.locator('[data-testid="session-delete-btn"]').click()
    await expect(dialog).toBeVisible()
    await dialog.getByRole('button', { name: 'Delete' }).click()

    // SSE refresh drops the row; fixture no longer has sess-004.
    await expect(page.locator('.sess')).toHaveCount(SEEDED_COUNT - 1, { timeout: 4000 })
    await expect(rowFor(page, 'scratch')).toHaveCount(0)
    snap = await (await request.get('/__fixture/snapshot')).json()
    expect(snap.items.some(i => i.session && i.session.id === 'sess-004')).toBe(false)
  })

  test('Worktree finish button shows merge confirm; confirm removes the session row', async ({ page, request }) => {
    await gotoSidebar(page)
    // Only sess-001 is seeded with worktreeBranch+worktreeRepoRoot, so
    // exactly one row carries the ⎇✓ button (dataModel.js worktree gate).
    const finishBtns = page.locator('[data-action="worktree-finish"]')
    await expect(finishBtns).toHaveCount(1)

    const row = rowFor(page, 'agent-deck')
    await row.hover()
    await row.locator('[data-action="worktree-finish"]').click()

    const dialog = page.locator('[role="dialog"]')
    await expect(dialog).toBeVisible()
    // Exact copy from Sidebar.js doAction('worktreeFinish'), including the
    // seeded branch name — the dialog must mention the merge.
    await expect(dialog).toContainText('Finish worktree for "agent-deck"?')
    await expect(dialog).toContainText('Merges branch "feat/fixture" into default branch')

    // Confirm (generic ConfirmDialog confirm label is "Delete"); the fixture
    // FinishWorktree removes the session, SSE refresh drops the row.
    await dialog.getByRole('button', { name: 'Delete' }).click()
    await expect(page.locator('.sess')).toHaveCount(SEEDED_COUNT - 1, { timeout: 4000 })
    await expect(rowFor(page, 'agent-deck')).toHaveCount(0)
    const snap = await (await request.get('/__fixture/snapshot')).json()
    expect(snap.items.some(i => i.session && i.session.id === 'sess-001')).toBe(false)
  })
})
