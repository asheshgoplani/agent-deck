// e2e/search-pane.spec.js -- Search pane end-to-end coverage.
//
// SearchPane (internal/web/static/app/panes/SearchPane.js) filters the
// in-memory session list by a case-insensitive substring over
// "title + path + tool + group". Assertions are grounded in the fixture seed
// (tests/web/fixtures/cmd/web-fixture/main.go seed()):
//
//   sess-001 "agent-deck"     tool=claude group=work           path=/srv/agent-deck
//   sess-002 "frontend"       tool=claude group=work           path=/srv/frontend
//   sess-003 "innotrade-api"  tool=codex  group=work/innotrade path=/srv/innotrade-api
//   sess-004 "scratch"        tool=shell  group=personal       path=/home/dev/scratch
//
// Reachability: the desktop/tablet Topbar has a Search tab, but the phone
// layout hides .top-tabs (app.css @media max-width:720px) and MobileTabs has
// no Search entry. The pane itself renders fine inside .main on every
// viewport, so most tests preseed localStorage('agentdeck.tab') — the same
// pattern as skills.spec.js gotoSkills — and run on all 3 projects. Only the
// Topbar-click navigation test is desktop/tablet-scoped.

import { test, expect } from '@playwright/test'

// Open the app directly on the Search tab. uiState.js initializes
// activeTabSignal from localStorage('agentdeck.tab'), so the init-script
// preseed works on every viewport, including the phone layout where no
// Search tab button exists.
async function gotoSearch(page) {
  await page.addInitScript(() => {
    localStorage.setItem('agentdeck.tab', JSON.stringify('search'))
  })
  await page.goto('/')
  await expect(page.locator('[data-testid="search-pane"]')).toBeVisible({ timeout: 5000 })
}

test.describe('search pane', () => {
  test.beforeEach(async ({ request }) => {
    await request.post('/__fixture/reset')
  })

  test('Topbar Search tab navigates to the search pane', async ({ page, viewport }) => {
    // desktop/tablet-only: .top-tabs is display:none at ≤720px and MobileTabs
    // offers no Search entry, so there is no Search nav affordance on phone.
    // Phone reachability is covered by the localStorage preseed used below.
    test.skip((viewport?.width || 1280) < 768, 'phone viewport: Topbar tabs hidden; Search uses localStorage preseed instead')

    await page.goto('/')
    await expect(page.locator('[data-testid="fleet-pane"]')).toBeVisible({ timeout: 5000 })
    await page.locator('.top-tab', { hasText: 'Search' }).click()
    await expect(page.locator('[data-testid="search-pane"]')).toBeVisible()
    await expect(page.locator('[data-testid="fleet-pane"]')).toHaveCount(0)
  })

  test('initially lists all seeded sessions', async ({ page }) => {
    await gotoSearch(page)
    // Empty query → SearchPane returns the full session list.
    const results = page.locator('[data-testid="search-result"]')
    await expect(results).toHaveCount(4)
    await expect(page.locator('[data-testid="search-result-count"]')).toContainText('4 MATCHES')
    for (const id of ['sess-001', 'sess-002', 'sess-003', 'sess-004']) {
      await expect(page.locator(`[data-testid="search-result"][data-session-id="${id}"]`)).toBeVisible()
    }
  })

  test('filters by title substring', async ({ page }) => {
    await gotoSearch(page)
    await page.locator('[data-testid="search-input"]').fill('frontend')
    const results = page.locator('[data-testid="search-result"]')
    await expect(results).toHaveCount(1)
    await expect(results.first()).toHaveAttribute('data-session-id', 'sess-002')
    await expect(results.first()).toContainText('frontend')
    await expect(page.locator('[data-testid="search-result-count"]')).toContainText('1 MATCH ·')
  })

  test('filters by tool name', async ({ page }) => {
    await gotoSearch(page)
    // 'codex' appears only as sess-003's tool (not in any title/path/group).
    await page.locator('[data-testid="search-input"]').fill('codex')
    const results = page.locator('[data-testid="search-result"]')
    await expect(results).toHaveCount(1)
    await expect(results.first()).toHaveAttribute('data-session-id', 'sess-003')
    await expect(results.first()).toContainText('innotrade-api')

    // 'claude' matches both claude-tool sessions (sess-001, sess-002).
    await page.locator('[data-testid="search-input"]').fill('claude')
    await expect(results).toHaveCount(2)
    await expect(page.locator('[data-testid="search-result"][data-session-id="sess-001"]')).toBeVisible()
    await expect(page.locator('[data-testid="search-result"][data-session-id="sess-002"]')).toBeVisible()
  })

  test('filters by group name', async ({ page }) => {
    await gotoSearch(page)
    // 'personal' appears only as sess-004's groupPath; its path
    // (/home/dev/scratch) and the other sessions' fields don't contain it.
    await page.locator('[data-testid="search-input"]').fill('personal')
    const results = page.locator('[data-testid="search-result"]')
    await expect(results).toHaveCount(1)
    await expect(results.first()).toHaveAttribute('data-session-id', 'sess-004')
    await expect(results.first()).toContainText('scratch')
  })

  test('filters by path substring', async ({ page }) => {
    await gotoSearch(page)
    // '/srv' prefixes the projectPath of sess-001/002/003; sess-004 lives
    // under /home/dev and must drop out.
    await page.locator('[data-testid="search-input"]').fill('/srv')
    const results = page.locator('[data-testid="search-result"]')
    await expect(results).toHaveCount(3)
    await expect(page.locator('[data-testid="search-result"][data-session-id="sess-004"]')).toHaveCount(0)

    // '/home/dev' isolates the one personal session.
    await page.locator('[data-testid="search-input"]').fill('/home/dev')
    await expect(results).toHaveCount(1)
    await expect(results.first()).toHaveAttribute('data-session-id', 'sess-004')
  })

  test('no-match query shows the zero-match state', async ({ page }) => {
    await gotoSearch(page)
    await page.locator('[data-testid="search-input"]').fill('zebra-unicorn-nope')
    // SearchPane has no dedicated empty-state component: it renders zero
    // result rows and the count line flips to "0 MATCHES".
    await expect(page.locator('[data-testid="search-result"]')).toHaveCount(0)
    await expect(page.locator('[data-testid="search-result-count"]')).toContainText('0 MATCHES')
    // Pane chrome (input + count) stays visible.
    await expect(page.locator('[data-testid="search-input"]')).toBeVisible()
  })

  test('clicking a result selects the session and switches to the Terminal tab', async ({ page }) => {
    await gotoSearch(page)
    await page.locator('[data-testid="search-input"]').fill('frontend')
    const result = page.locator('[data-testid="search-result"][data-session-id="sess-002"]')
    await expect(result).toBeVisible()
    await result.click()

    // SearchPane.onSelect sets selectedIdSignal + activeTabSignal='terminal':
    // the search pane unmounts, the always-mounted terminal wrapper becomes
    // visible, and the work-head breadcrumb shows the selected title.
    await expect(page.locator('[data-testid="search-pane"]')).toHaveCount(0)
    await expect(page.locator('.term-wrap')).toBeVisible()
    await expect(page.locator('.work-head .cur')).toHaveText('frontend')
  })
})
