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

// The tablet project's open right rail leaves the board too narrow for the
// Status | Groups toggle (app.css @container rule); hiding the rail makes room.
async function showBoardToggle(page) {
  const toggle = page.locator('[data-testid="fleet-view-status"]')
  if (!(await toggle.isVisible())) await page.getByRole('button', { name: 'Toggle right rail' }).click()
  await expect(toggle).toBeVisible()
}

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

  test('configured remotes render through the production API alongside local groups', async ({ page, request }) => {
    await request.post('/__fixture/remotes')
    await page.goto('/')
    await expect(page.locator('[data-testid="fleet-remote-card"]')).toHaveCount(2)
    await expect(page.locator('[data-testid="fleet-remote-card"][data-remote-name="build"]'))
      .toContainText('24ms')
    await expect(page.locator('[data-testid="fleet-remote-session-tile"]')).toHaveCount(3)
    await expect(page.locator('[data-testid="fleet-remote-card"][data-remote-name="offline"]')).toContainText('last-known')
    await expect(page.locator('[data-testid="fleet-remote-age"]')).toHaveText('Last known state · 37s ago')
    await expect(page.locator('[data-testid="fleet-stat-remotes"]')).toHaveText('1/2 remotes online')
    await expect(page.locator('[data-testid="fleet-stat-sessions"] .num')).toHaveText('7')
    // Runtime tiles add the remotes' counts (fixture: 1 running, 1 waiting,
    // 1 idle) to the local ones.
    await expect(page.locator('[data-testid="fleet-stat-running"] .num')).toHaveText('2')
    await expect(page.locator('[data-testid="fleet-stat-waiting"] .num')).toHaveText('1')
    await expect(page.locator('[data-testid="fleet-stat-idle"] .num')).toHaveText('4')
    await expect(page.locator('[data-testid="fleet-group-card"]')).toHaveCount(3)
  })

  // The toggle sits between the GROUPS kicker and the right-aligned
  // sub-kicker without growing the head, so the default board looks as it
  // did before the toggle: the sub-kicker stays flush right on one line and
  // the group grid does not move (visual-baselines.spec.js home.png).
  test('the Status | Groups toggle keeps the section head height and the sub-kicker in place', async ({ page }) => {
    await page.goto('/')
    await expect(page.locator('[data-testid="fleet-group-card"]')).toHaveCount(3)
    const g = await page.evaluate(() => {
      const head = document.querySelector('[data-testid="fleet-view-groups"]').closest('.fleet-section-head')
      const box = el => el.getBoundingClientRect()
      const toggle = head.querySelector('.fleet-view-toggle')
      return {
        head: box(head), kicker: box(head.querySelector('.kicker')), sub: box(head.querySelector('.sub-kicker')),
        padBottom: parseFloat(getComputedStyle(head).paddingBottom),
        toggle: toggle && getComputedStyle(toggle).display !== 'none' ? box(toggle) : null,
      }
    })
    expect(g.head.height).toBeLessThanOrEqual(Math.max(g.kicker.height, g.sub.height) + g.padBottom + 0.5)
    expect(Math.abs(g.head.right - g.sub.right)).toBeLessThan(0.5)
    if (g.toggle) {
      expect(g.toggle.left).toBeGreaterThan(g.kicker.right)
      expect(g.toggle.right).toBeLessThan(g.sub.left)
    }
  })

  // At tablet width the open right rail leaves the board about 150px wide:
  // no room for the toggle beside the kicker and sub-kicker, so it steps
  // aside until the board is wider.
  test('a board too narrow for the toggle hides it until the rail is closed', async ({ page }) => {
    await page.setViewportSize({ width: 820, height: 1180 })
    await page.goto('/')
    await expect(page.locator('[data-testid="fleet-group-card"]')).toHaveCount(3)
    await expect(page.locator('body')).toHaveAttribute('data-rail', 'visible')
    await expect(page.locator('[data-testid="fleet-view-status"]')).toBeHidden()
    await page.getByRole('button', { name: 'Toggle right rail' }).click()
    await expect(page.locator('[data-testid="fleet-view-status"]')).toBeVisible()
  })

  // The Fleet board's layout is a per-browser choice (agentdeck.fleetView).
  // A viewer who never touched the Status | Groups toggle keeps the group
  // grid; the semantic-status kanban is opt-in.
  test('Groups is the default board view; the status kanban stays unloaded', async ({ page }) => {
    await page.goto('/')
    await expect(page.locator('[data-testid="fleet-group-card"]')).toHaveCount(3)
    await expect(page.locator('[data-testid="fleet-view-groups"]')).toHaveAttribute('aria-pressed', 'true')
    await expect(page.locator('[data-testid="fleet-view-status"]')).toHaveAttribute('aria-pressed', 'false')
    await expect(page.locator('[data-testid="fleet-kanban"]')).toHaveCount(0)
    await expect(page.locator('[data-testid="conductor-banner"]')).toHaveCount(0)
    await expect(page.locator('[data-testid="fleet-status-stats"]')).toHaveCount(0)
    await expect(page.locator('[data-testid="fleet-stat-running"] .num')).toHaveText('1')
    expect(await page.evaluate(() => localStorage.getItem('agentdeck.fleetView'))).toBe('"groups"')
  })

  test('Status view is opt-in: kanban, semantic tiles and conductor banner, persisted across reloads', async ({ page }) => {
    await page.goto('/')
    await expect(page.locator('[data-testid="fleet-group-card"]')).toHaveCount(3)
    await showBoardToggle(page)
    await page.locator('[data-testid="fleet-view-status"]').click()

    // Seed: sess-001 is a conductor (pinned in the banner, out of the
    // columns); the other three carry no status hint, so they are untriaged.
    await expect(page.locator('[data-testid="fleet-kanban"]')).toBeVisible()
    await expect(page.locator('[data-testid="fleet-group-card"]')).toHaveCount(0)
    await expect(page.locator('[data-testid="conductor-banner"]')).toHaveAttribute('data-session-id', 'sess-001')
    await expect(page.locator('[data-testid="kanban-col-untriaged"] [data-testid="kanban-card"]')).toHaveCount(3)
    await expect(page.locator('[data-testid="fleet-stat-untriaged"] .num')).toHaveText('3')
    await expect(page.locator('[data-testid="fleet-stat-needs-input"] .num')).toHaveText('0')
    // The runtime tiles stay next to the semantic ones.
    await expect(page.locator('[data-testid="fleet-stat-running"] .num')).toHaveText('1')
    await expect(page.locator('[data-testid="fleet-stat-idle"] .num')).toHaveText('3')

    await page.reload()
    await expect(page.locator('[data-testid="fleet-kanban"]')).toBeVisible()
    await expect(page.locator('[data-testid="fleet-view-status"]')).toHaveAttribute('aria-pressed', 'true')

    await page.locator('[data-testid="fleet-view-groups"]').click()
    await expect(page.locator('[data-testid="fleet-group-card"]')).toHaveCount(3)
    await expect(page.locator('[data-testid="fleet-kanban"]')).toHaveCount(0)
    await expect(page.locator('[data-testid="conductor-banner"]')).toHaveCount(0)
  })
})
