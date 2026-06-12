// e2e/fleet-pane.spec.js -- Fleet pane (default tab) end-to-end coverage.
//
// The Fleet pane (internal/web/static/app/panes/FleetPane.js) is the cold-load
// landing surface: activeTabSignal defaults to 'fleet' (uiState.js). It renders
// stat tiles computed from menuModelSignal plus one GroupCard per non-empty
// group. All assertions are grounded in the fixture seed
// (tests/web/fixtures/cmd/web-fixture/main.go seed()):
//
//   sess-001 "agent-deck"     tool=claude status=idle    group=work           path=/srv/agent-deck
//   sess-002 "frontend"       tool=claude status=running group=work           path=/srv/frontend
//   sess-003 "innotrade-api"  tool=codex  status=idle    group=work/innotrade path=/srv/innotrade-api
//   sess-004 "scratch"        tool=shell  status=idle    group=personal       path=/home/dev/scratch
//
// → counts: running=1, waiting=0, error=0, idle=3, sessions=4
// → groups (labels are uppercased by dataModel.js projectGroup):
//     WORK (2 sessions), INNOTRADE (1 session), PERSONAL (1 session)
//
// The Fleet pane renders on ALL viewports (phone gets dedicated CSS tweaks in
// app.css @media (max-width: 720px) and a Fleet entry in MobileTabs), so no
// phone skips here — every test runs on chromium-desktop/tablet/phone.

import { test, expect } from '@playwright/test'

test.describe('fleet pane', () => {
  test.beforeEach(async ({ request }) => {
    await request.post('/__fixture/reset')
  })

  test('cold load lands on the Fleet tab', async ({ page }) => {
    // Fresh browser context → no persisted agentdeck.tab in localStorage →
    // activeTabSignal falls back to its 'fleet' default.
    await page.goto('/')
    await expect(page.locator('[data-testid="fleet-pane"]')).toBeVisible({ timeout: 5000 })
    // The always-mounted terminal pane stays CSS-hidden while Fleet is active.
    await expect(page.locator('.term-wrap')).toBeHidden()
  })

  test('stat tiles show counts derived from the fixture seed', async ({ page }) => {
    await page.goto('/')
    await expect(page.locator('[data-testid="fleet-pane"]')).toBeVisible({ timeout: 5000 })
    // Seed: sess-002 running; sess-001/003/004 idle; nothing waiting/error.
    // toHaveText retries, which absorbs the initial empty render before the
    // first SSE menu snapshot hydrates sessionsSignal.
    await expect(page.locator('[data-testid="fleet-stat-running"] .num')).toHaveText('1')
    await expect(page.locator('[data-testid="fleet-stat-waiting"] .num')).toHaveText('0')
    await expect(page.locator('[data-testid="fleet-stat-error"] .num')).toHaveText('0')
    await expect(page.locator('[data-testid="fleet-stat-idle"] .num')).toHaveText('3')
    await expect(page.locator('[data-testid="fleet-stat-sessions"] .num')).toHaveText('4')
  })

  test('group cards render seeded groups with session count footers', async ({ page }) => {
    await page.goto('/')
    await expect(page.locator('[data-testid="fleet-pane"]')).toBeVisible({ timeout: 5000 })

    const cards = page.locator('[data-testid="fleet-group-card"]')
    await expect(cards).toHaveCount(3)

    // dataModel.js projectGroup uppercases names into labels; GroupCard
    // receives that label as `name` and we mirror it into data-group-name.
    const work = page.locator('[data-testid="fleet-group-card"][data-group-name="WORK"]')
    const innotrade = page.locator('[data-testid="fleet-group-card"][data-group-name="INNOTRADE"]')
    const personal = page.locator('[data-testid="fleet-group-card"][data-group-name="PERSONAL"]')

    await expect(work).toBeVisible()
    await expect(work.locator('[data-testid="fleet-group-session-count"]')).toHaveText('2 sessions')
    // work holds the seed's agent-deck + frontend tiles.
    await expect(work.locator('[data-testid="fleet-session-tile"]')).toHaveCount(2)

    await expect(innotrade).toBeVisible()
    await expect(innotrade.locator('[data-testid="fleet-group-session-count"]')).toHaveText('1 session')

    await expect(personal).toBeVisible()
    await expect(personal.locator('[data-testid="fleet-group-session-count"]')).toHaveText('1 session')
  })

  test('clicking a session tile selects it and switches to the Terminal tab', async ({ page }) => {
    await page.goto('/')
    await expect(page.locator('[data-testid="fleet-pane"]')).toBeVisible({ timeout: 5000 })

    const tile = page.locator('[data-testid="fleet-session-tile"][data-session-id="sess-003"]')
    await expect(tile).toBeVisible()
    await expect(tile).toContainText('innotrade-api')
    await tile.click()

    // FleetPane.onSelect sets selectedIdSignal=sess-003 + activeTabSignal='terminal':
    // the fleet pane unmounts, the terminal wrapper becomes visible, and the
    // work-head breadcrumb shows the selected session's title. These checks
    // hold on phone too (work-head + term-wrap survive the ≤720px layout).
    await expect(page.locator('[data-testid="fleet-pane"]')).toHaveCount(0)
    await expect(page.locator('.term-wrap')).toBeVisible()
    await expect(page.locator('.work-head .cur')).toHaveText('innotrade-api')
  })

  test('live update: status change is reflected in stat tiles within ~2s', async ({ page, request }) => {
    await page.goto('/')
    await expect(page.locator('[data-testid="fleet-pane"]')).toBeVisible({ timeout: 5000 })

    // Pin the starting state so the post-mutation assertion can't false-pass.
    await expect(page.locator('[data-testid="fleet-stat-waiting"] .num')).toHaveText('0')
    await expect(page.locator('[data-testid="fleet-stat-idle"] .num')).toHaveText('3')

    // Simulate a TUI-side transition through the fixture admin endpoint.
    // This bypasses the web mutator (no immediate SSE broadcast), so the
    // change rides the menu stream's 2s poll tick (handlers_events.go
    // menuEventsPollInterval) — 4s is a comfortable bound, same spirit as
    // the children-panel live-update test.
    const res = await request.post('/__fixture/session/sess-001/status?to=waiting')
    expect(res.status()).toBe(204)

    await expect(page.locator('[data-testid="fleet-stat-waiting"] .num')).toHaveText('1', { timeout: 4000 })
    await expect(page.locator('[data-testid="fleet-stat-idle"] .num')).toHaveText('2')
    // Untouched tiles stay put.
    await expect(page.locator('[data-testid="fleet-stat-running"] .num')).toHaveText('1')
    await expect(page.locator('[data-testid="fleet-stat-sessions"] .num')).toHaveText('4')
  })
})
