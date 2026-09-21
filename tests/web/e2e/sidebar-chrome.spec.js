// e2e/sidebar-chrome.spec.js -- Sidebar chrome coverage: status filter chips,
// column show/hide menu, group collapse, and the side-filter input.
//
// All assertions are grounded in the fixture seed (tests/web/fixtures/cmd/
// web-fixture/main.go seed()):
//   sess-001 "agent-deck"    tool=claude status=idle    group=work
//   sess-002 "frontend"      tool=claude status=running group=work
//   sess-003 "innotrade-api" tool=codex  status=idle    group=work/innotrade
//   sess-004 "scratch"       tool=shell  status=idle    group=personal
//
// Notes on intentionally-locked-in behavior (audited against Sidebar.js):
//   - Group expand/collapse lives in uiState.groupExpandedSignal and DOES
//     persist across reload (localStorage key `agentdeck.groupExpanded`).
//   - The seeded `personal` group has Expanded:false and the web DOES honor
//     the server's `expanded` field: PATCH /api/groups/{path} {expanded}
//     writes it back, so adopting it no longer leaks TUI collapse one-way.
//     `personal` therefore renders collapsed on a fresh load, hiding
//     `scratch`; gotoSidebar() expands it so all 4 seeded sessions are
//     visible (see helpers/seededSidebar.js).
//   - Column visibility persists via localStorage key `agentdeck.showCols`
//     (uiState.js showColsSignal + persist()).
//   - Sidebar width: state.js exports sidebarWidthSignal (localStorage key
//     `sidebar-width`, clamp [200,480]) but NOTHING in the redesigned shell
//     consumes it — the `.app` grid uses the static CSS var --sidebar-w and
//     Sidebar.js has no drag handle. There is no width behavior to test, so
//     no width spec here; if a consumer lands, add a preseed+reload test.
//
// Phone (<768px) skips: the sidebar is desktop/tablet-only (same pattern as
// keyboard-parity.spec.js / skills.spec.js).

import { test, expect } from '@playwright/test'
import { expandSeededCollapsedGroups } from '../helpers/seededSidebar.js'

// Seed-derived expectations.
const ALL_TITLES = ['agent-deck', 'frontend', 'innotrade-api', 'scratch']
const RUNNING_TITLES = ['frontend']
const IDLE_TITLES = ['agent-deck', 'innotrade-api', 'scratch']

async function gotoSidebar(page) {
  await page.goto('/')
  // Sidebar list takes the initial /api/menu fetch + render to populate.
  await expandSeededCollapsedGroups(page)
  await expect(page.locator('.sess')).toHaveCount(ALL_TITLES.length, { timeout: 5000 })
}

test.describe('sidebar chrome', () => {
  test.skip(({ viewport }) => (viewport?.width || 1280) < 768, 'phone viewport: sidebar is desktop/tablet only')

  test.beforeEach(async ({ page, request }) => {
    await request.post('/__fixture/reset')
    await gotoSidebar(page)
  })

  test('running status chip filters the list to running sessions only', async ({ page }) => {
    const chip = page.locator('[data-testid="status-chip-running"]')
    await chip.click()
    await expect(chip).toHaveClass(/\bon\b/)
    // Only sess-002 "frontend" is seeded running.
    await expect(page.locator('.sess')).toHaveCount(RUNNING_TITLES.length)
    await expect(page.locator('.sess .tt')).toHaveText(RUNNING_TITLES)
    // The side-head visible-session count tracks the filter.
    await expect(page.locator('.side-head .count')).toHaveText(String(RUNNING_TITLES.length))
  })

  test('combining status chips unions; re-clicking clears each filter', async ({ page }) => {
    const running = page.locator('[data-testid="status-chip-running"]')
    const idle = page.locator('[data-testid="status-chip-idle"]')

    // running only → 1 row.
    await running.click()
    await expect(page.locator('.sess')).toHaveCount(RUNNING_TITLES.length)

    // running + idle → union covers all 4 seeded sessions.
    await idle.click()
    await expect(idle).toHaveClass(/\bon\b/)
    await expect(page.locator('.sess')).toHaveCount(ALL_TITLES.length)

    // re-click running → idle-only → 3 rows.
    await running.click()
    await expect(running).not.toHaveClass(/\bon\b/)
    await expect(page.locator('.sess')).toHaveCount(IDLE_TITLES.length)
    await expect(page.locator('.sess .tt')).toHaveText(IDLE_TITLES)

    // re-click idle → no filters → all rows back.
    await idle.click()
    await expect(page.locator('.sess')).toHaveCount(ALL_TITLES.length)
  })

  test('column menu: toggling Tool badge off hides tags and persists across reload', async ({ page }) => {
    // showCols default has tool:true and every seeded session has a tool,
    // so each row renders a `.tag` badge.
    await expect(page.locator('.sess .tag')).toHaveCount(ALL_TITLES.length)

    await page.locator('[data-testid="show-cols-btn"]').click()
    await expect(page.locator('[data-testid="show-cols-menu"]')).toBeVisible()
    const toolRow = page.locator('[data-testid="show-col-tool"]')
    await expect(toolRow.locator('input')).toBeChecked()
    await toolRow.locator('input').click()

    // Tool badges disappear from every row immediately.
    await expect(page.locator('.sess .tag')).toHaveCount(0)

    // Persistence: showColsSignal writes localStorage `agentdeck.showCols`.
    const stored = await page.evaluate(() => JSON.parse(localStorage.getItem('agentdeck.showCols')))
    expect(stored.tool).toBe(false)

    // Reload restores the persisted choice.
    await gotoSidebar(page)
    await expect(page.locator('.sess .tag')).toHaveCount(0)
    await expect(page.locator('.sess')).toHaveCount(ALL_TITLES.length) // rows themselves unaffected
  })

  test('group collapse hides member sessions; expand restores; persists across reload', async ({ page }) => {
    const workHead = page.locator('[data-testid="group-head-work"]')
    const workChev = page.locator('[data-testid="group-chev-work"]')

    // Collapse "work" → its own members (agent-deck, frontend) disappear AND
    // so does the `work/innotrade` subtree, because visibility is a property
    // of the whole ancestor chain, not just a group's own flag. This used to
    // leave innotrade-api on screen under a header whose parent was collapsed
    // — the web-side shape of issue #1878.
    await workChev.click()
    await expect(workHead.locator('.chev')).toHaveText('▸')
    await expect(page.locator('.sess')).toHaveCount(1)
    await expect(page.locator('.sess .tt')).toHaveText(['scratch'])
    await expect(page.locator('[data-testid="group-head-work/innotrade"]')).toHaveCount(0)

    // Expand restores the members, and the subgroup with them.
    await workChev.click()
    await expect(workHead.locator('.chev')).toHaveText('▾')
    await expect(page.locator('.sess')).toHaveCount(ALL_TITLES.length)

    // Collapse again, then reload. Collapse is now persisted server-side via
    // PATCH /api/groups/{path} {expanded} as well as mirrored into
    // localStorage `agentdeck.groupExpanded`, so it survives the reload from
    // either source.
    await workChev.click()
    await expect(page.locator('.sess')).toHaveCount(1)
    const stored = await page.evaluate(() => JSON.parse(localStorage.getItem('agentdeck.groupExpanded')))
    expect(stored.work).toBe(false)

    await page.goto('/')
    await expect(page.locator('[data-testid="group-head-work"] .chev')).toHaveText('▸', { timeout: 5000 })
    await expect(page.locator('.sess')).toHaveCount(1)
  })

  // The chip set mirrors the five status buckets the group panel uses. A
  // `stopped` chip was missing entirely, so a parked session could not be
  // filtered for from the web at all -- the TUI surfaces stopped with the same
  // filled-square glyph.
  test('stopped chip exists and filters to parked sessions', async ({ page }) => {
    const chip = page.locator('[data-testid="status-chip-stopped"]')
    await expect(chip).toHaveCount(1)
    await expect(chip).toHaveText('■')

    // No stopped sessions in the seed, so the chip alone yields an empty list.
    await chip.click()
    await expect(page.locator('.sess')).toHaveCount(0)
    await chip.click()

    // Park one for real through the same endpoint the Stop button uses, then
    // restore it so sibling tests keep the 4-session seed.
    await page.request.post('/api/sessions/sess-002/stop')
    try {
      await gotoSidebar(page)
      await chip.click()
      await expect(page.locator('.sess')).toHaveCount(1)
      await expect(page.locator('.sess .tt')).toHaveText(['frontend'])
    } finally {
      await page.request.post('/api/sessions/sess-002/start')
    }
  })

  test('chips sit in status-bucket order', async ({ page }) => {
    await expect(page.locator('.side-filter .side-chip')).toHaveText(['●', '◐', '○', '■', '✕'])
  })

  test('side-filter input filters rows by title and hides empty groups', async ({ page }) => {
    const input = page.locator('[data-testid="sidebar-filter-input"]')
    await input.fill('front')

    // Matches title "frontend" (matcher also scans group/path/tool/branch;
    // "front" only appears in sess-002's title + /srv/frontend path).
    await expect(page.locator('.sess')).toHaveCount(1)
    await expect(page.locator('.sess .tt')).toHaveText(['frontend'])
    await expect(page.locator('.side-head .count')).toHaveText('1')

    // Groups with zero matches are dropped entirely while a text filter is
    // active (Sidebar.js: `if (filter && members.length === 0) return null`).
    await expect(page.locator('[data-testid="group-head-personal"]')).toHaveCount(0)
    await expect(page.locator('[data-testid="group-head-work"]')).toHaveCount(1)

    // Clearing the filter restores all rows and group heads.
    await input.fill('')
    await expect(page.locator('.sess')).toHaveCount(ALL_TITLES.length)
    await expect(page.locator('[data-testid="group-head-personal"]')).toHaveCount(1)
  })

  test('side-filter matches tool names too (distinct from SearchPane)', async ({ page }) => {
    // The side-filter haystack is title+group+path+tool+branch; "codex" only
    // matches sess-003's tool. This pins the sidebar-local filter contract.
    await page.locator('[data-testid="sidebar-filter-input"]').fill('codex')
    await expect(page.locator('.sess')).toHaveCount(1)
    await expect(page.locator('.sess .tt')).toHaveText(['innotrade-api'])
  })
})
