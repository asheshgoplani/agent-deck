// e2e/command-center.spec.js -- Command Center pane end-to-end coverage.
//
// The Command Center (internal/web/static/app/panes/CommandCenterPane.js) is the
// embedded, live, two-way fleet god-view. It renders the synthesized snapshot
// from GET /events/command-center (SSE), groups sessions per conductor, filters
// OUT error/stopped sessions (the noise the user explicitly rejected), surfaces
// honest-status substates, and routes typed instructions via POST
// /api/command-center/ask. See conductor/agent-deck/COMMAND-CENTER-DESIGN.md.
//
// Grounded in the fixture seed (tests/web/fixtures/cmd/web-fixture/main.go):
//   sess-001 "agent-deck"     status=idle    group=work           (IsConductor)
//   sess-002 "frontend"       status=running group=work
//   sess-003 "innotrade-api"  status=idle    group=work/innotrade
//   sess-004 "scratch"        status=idle    group=personal
// → active CHILD totals at cold load (the conductor row itself is excluded from
//   the per-child tally): running=1 (sess-002), waiting=0, idle=2 (sess-003/004).
import { test, expect } from '@playwright/test'

async function openCommandCenter(page) {
  await page.goto('/')
  // The top tab strip is hidden on phone-class viewports (≤720px), which use
  // the bottom MobileTabs bar instead. Click whichever control is visible so
  // the flagship view is reachable on every viewport.
  const viewport = page.viewportSize()
  if (viewport && viewport.width < 768) {
    await page.locator('[data-testid="mobile-tab-command-center"]').click()
  } else {
    await page.locator('.top-tab', { hasText: 'Command Center' }).click()
  }
  await expect(page.locator('[data-testid="command-center-pane"]')).toBeVisible({ timeout: 5000 })
}

test.describe('command center pane', () => {
  test.beforeEach(async ({ request }) => {
    await request.post('/__fixture/reset')
  })

  test('renders live with totals derived from the fixture seed', async ({ page }) => {
    await openCommandCenter(page)
    // The live indicator and totals hydrate from the first SSE snapshot.
    await expect(page.locator('[data-testid="cc-live"]')).toContainText('live', { timeout: 5000 })
    // Seed (child sessions only; the conductor row is tallied separately):
    // sess-002 running; sess-003/004 idle; nothing waiting. toContainText
    // retries through the initial empty render before the SSE snapshot lands.
    await expect(page.locator('[data-testid="cc-totals"]')).toContainText('1 running', { timeout: 5000 })
    await expect(page.locator('[data-testid="cc-totals"]')).toContainText('0 waiting')
    await expect(page.locator('[data-testid="cc-totals"]')).toContainText('2 idle')
  })

  test('shows conductor rows and the two-way input bar', async ({ page }) => {
    await openCommandCenter(page)
    // At least one conductor row renders (groups: work, work/innotrade, personal).
    await expect(page.locator('[data-testid="cc-conductor"]').first()).toBeVisible({ timeout: 5000 })
    // The two-way input bar and target picker are present.
    await expect(page.locator('[data-testid="cc-input"]')).toBeVisible()
    await expect(page.locator('[data-testid="cc-target"]')).toBeVisible()
    // Maestro is the default routing target.
    await expect(page.locator('[data-testid="cc-target"]')).toContainText('Maestro')
  })

  test('updates live via SSE without a reload', async ({ page, request }) => {
    await openCommandCenter(page)
    await expect(page.locator('[data-testid="cc-totals"]')).toContainText('1 running', { timeout: 5000 })

    // Drive a TUI-side transition: sess-002 running -> waiting. This must flow
    // through /events/command-center and re-render the panel WITHOUT a reload.
    await request.post('/__fixture/session/sess-002/status?to=waiting')

    await expect(page.locator('[data-testid="cc-totals"]')).toContainText('0 running', { timeout: 6000 })
    await expect(page.locator('[data-testid="cc-totals"]')).toContainText('1 waiting')
    // We never called page.goto/reload — the SSE feed alone drove the change.
  })

  test('filters OUT error/stopped sessions (the rejected noise)', async ({ page, request }) => {
    await openCommandCenter(page)
    await expect(page.locator('[data-testid="command-center-pane"]')).toBeVisible({ timeout: 5000 })

    // Force sess-002 to error. It must NOT appear in any conductor's session
    // list — error is filtered by construction.
    await request.post('/__fixture/session/sess-002/status?to=error')

    // Give the SSE feed a moment to push the change.
    await expect(page.locator('[data-testid="cc-totals"]')).toContainText('0 running', { timeout: 6000 })

    // Expand every conductor row, then assert no rendered session row is error/stopped.
    const heads = page.locator('.cc-cd-head')
    const n = await heads.count()
    for (let i = 0; i < n; i++) await heads.nth(i).click()

    const errorRows = page.locator('[data-testid="cc-session"][data-status="error"]')
    const stoppedRows = page.locator('[data-testid="cc-session"][data-status="stopped"]')
    await expect(errorRows).toHaveCount(0)
    await expect(stoppedRows).toHaveCount(0)
  })

  test('typing routes through the ask endpoint and reflects status', async ({ page }) => {
    await openCommandCenter(page)
    await expect(page.locator('[data-testid="cc-input"]')).toBeVisible({ timeout: 5000 })

    await page.locator('[data-testid="cc-input"] textarea').fill('approve #1361 — keep conductor.enabled')
    await page.locator('[data-testid="cc-send"]').click()

    // The status line reflects the routing outcome. In the fixture there is no
    // live agent behind the target, so the supported `session send` path can't
    // deliver and the handler returns an honest error — the point is that the
    // request went through the /ask endpoint and the UI reflected a result,
    // not a silent no-op. Either ✓ routed or ✗ deliver is a valid reflection.
    await expect(page.locator('[data-testid="cc-status"]')).not.toHaveText('ready', { timeout: 6000 })
  })
})
