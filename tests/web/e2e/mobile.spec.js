// e2e/mobile.spec.js -- POSITIVE phone-viewport coverage (chromium-phone).
//
// Other specs SKIP on phone; this one runs ONLY there and pins the phone
// layout contract from app.css `@media (max-width: 720px)` plus
// MobileTabs.js / FleetPane.js behavior:
//
//   - `.mob-tabs` (display:none by default, display:grid ≤720px) is the
//     bottom navigation: Fleet / Session(terminal) / Watchers / Costs —
//     the MOBILE_TABS set in MobileTabs.js. Tapping a tab writes
//     activeTabSignal, the same signal the desktop top-tabs use.
//   - `.sidebar`, `.rightrail`, `.footer`, `.top-tabs`, `.top-search` are
//     all display:none on phone. The `.topbar` itself stays (brand).
//   - With the sidebar hidden, the ONLY phone path to a session is the
//     Fleet pane's session tiles ([data-testid="fleet-session-tile"],
//     FleetPane.js onSelect → selectedIdSignal + activeTab='terminal').
//     There is no hamburger/drawer for the sidebar — grid column is 0 and
//     the element is display:none (verified in app.css).
//   - Cold load lands on the Fleet tab (uiState.js activeTabSignal default
//     'fleet'), so the phone entry surface is usable immediately.
//
// Fixture seed (tests/web/fixtures/cmd/web-fixture/main.go seed()):
// sess-001 "agent-deck" … sess-004 "scratch"; costs endpoints intentionally
// degrade to 503 → CostDashboard renders "Cost tracking unavailable".

import { test, expect } from '@playwright/test'

async function waitForPhoneMount(page) {
  // Phone cold load lands on Fleet (default tab) — the fleet pane appearing
  // means both the app shell mounted and the first menu snapshot arrived
  // (stat tiles/group cards render from menuModelSignal).
  await expect(page.locator('[data-testid="fleet-pane"]')).toBeVisible({ timeout: 5000 })
  await expect(page.locator('[data-testid="fleet-group-card"]').first()).toBeVisible({ timeout: 5000 })
}

test.describe('mobile phone layout', () => {
  test.skip(
    ({ viewport }) => (viewport?.width || 1280) >= 768,
    'phone-only positive coverage: desktop/tablet layouts are covered by the other specs',
  )

  test.beforeEach(async ({ page, request }) => {
    await request.post('/__fixture/reset')
    await page.goto('/')
    await waitForPhoneMount(page)
  })

  test('bottom tab bar is visible with the Fleet/Session/Watchers/Costs set', async ({ page }) => {
    const bar = page.locator('[data-testid="mobile-tabs"]')
    await expect(bar).toBeVisible()
    // Exact tab set + labels from MOBILE_TABS in MobileTabs.js.
    await expect(bar.locator('.mob-tab')).toHaveCount(4)
    await expect(page.locator('[data-testid="mobile-tab-fleet"]')).toContainText('Fleet')
    await expect(page.locator('[data-testid="mobile-tab-terminal"]')).toContainText('Session')
    await expect(page.locator('[data-testid="mobile-tab-watchers"]')).toContainText('Watchers')
    await expect(page.locator('[data-testid="mobile-tab-costs"]')).toContainText('Costs')
    // Cold load default tab is fleet → its tab carries the `on` class.
    await expect(page.locator('[data-testid="mobile-tab-fleet"]')).toHaveClass(/\bon\b/)
  })

  test('tapping each mobile tab switches the active pane', async ({ page }) => {
    // Session (terminal): no session selected yet → TerminalPanel renders
    // the EmptyStateDashboard inside the .term-wrap chrome.
    await page.locator('[data-testid="mobile-tab-terminal"]').click()
    await expect(page.locator('[data-testid="mobile-tab-terminal"]')).toHaveClass(/\bon\b/)
    await expect(page.locator('.term-wrap')).toBeVisible()
    await expect(page.locator('[data-testid="empty-state-dashboard"]')).toBeVisible()
    await expect(page.locator('[data-testid="fleet-pane"]')).toHaveCount(0)

    // Watchers: StubPane with the documented TUI-only message.
    await page.locator('[data-testid="mobile-tab-watchers"]').click()
    await expect(page.locator('[data-testid="mobile-tab-watchers"]')).toHaveClass(/\bon\b/)
    await expect(page.locator('.chart-card .title', { hasText: 'Watchers' })).toBeVisible()
    await expect(page.locator('.term-wrap')).toBeHidden()

    // Costs: fixture has no cost store (503) → CostDashboard's degraded card.
    await page.locator('[data-testid="mobile-tab-costs"]').click()
    await expect(page.locator('[data-testid="mobile-tab-costs"]')).toHaveClass(/\bon\b/)
    await expect(page.locator('.chart-card .title', { hasText: 'Cost tracking unavailable' })).toBeVisible()

    // Back to Fleet.
    await page.locator('[data-testid="mobile-tab-fleet"]').click()
    await expect(page.locator('[data-testid="mobile-tab-fleet"]')).toHaveClass(/\bon\b/)
    await expect(page.locator('[data-testid="fleet-pane"]')).toBeVisible()
  })

  test('desktop chrome is CSS-hidden: top tabs, search, sidebar, right rail, footer', async ({ page }) => {
    // app.css @media (max-width: 720px) hides these; assert computed
    // visibility, not mere existence — they all stay in the DOM.
    await expect(page.locator('.top-tabs')).toBeHidden()
    await expect(page.locator('.top-search')).toBeHidden()
    await expect(page.locator('.sidebar')).toBeHidden()
    await expect(page.locator('[data-testid="right-rail"]')).toBeHidden()
    await expect(page.locator('.footer')).toBeHidden()
    // The topbar itself survives (brand block), as does the bottom bar.
    await expect(page.locator('.topbar')).toBeVisible()
    await expect(page.locator('.top-brand')).toBeVisible()
    await expect(page.locator('[data-testid="mobile-tabs"]')).toBeVisible()
    // Belt and braces: the sidebar is really display:none (no drawer variant).
    const sidebarDisplay = await page
      .locator('.sidebar')
      .evaluate(el => getComputedStyle(el).display)
    expect(sidebarDisplay).toBe('none')
  })

  test('phone session-selection flow: Fleet tile → terminal pane', async ({ page }) => {
    // The sidebar (the desktop selection surface) is display:none on phone
    // and there is no hamburger, so Fleet tiles are the supported flow.
    const tile = page.locator('[data-testid="fleet-session-tile"][data-session-id="sess-001"]')
    await expect(tile).toBeVisible()
    await tile.click()
    // FleetPane.onSelect sets selectedIdSignal + activeTab='terminal':
    // the bottom bar flips to Session and the terminal pane shows.
    await expect(page.locator('[data-testid="mobile-tab-terminal"]')).toHaveClass(/\bon\b/)
    await expect(page.locator('.term-wrap')).toBeVisible()
    // App.js routing mirrors the selection into the URL.
    await expect.poll(() => new URL(page.url()).pathname).toBe('/s/sess-001')
  })

  test('terminal pane renders the selected session frame on phone', async ({ page }) => {
    await page.locator('[data-testid="fleet-session-tile"][data-session-id="sess-002"]').click()
    await expect(page.locator('.term-wrap')).toBeVisible()
    // TerminalPanel renders the .term-frame chrome with the session id in
    // its strip once a session is selected (no EmptyStateDashboard).
    await expect(page.locator('.term-frame')).toBeVisible()
    await expect(page.locator('.term-strip .tpath')).toContainText('sess-002')
    await expect(page.locator('[data-testid="empty-state-dashboard"]')).toHaveCount(0)
  })
})
