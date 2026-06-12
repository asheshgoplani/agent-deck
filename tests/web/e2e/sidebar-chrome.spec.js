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
//   - Group expand/collapse is plain useState — it does NOT persist across
//     reload (no localStorage key). The reload assertion pins that.
//   - The seeded `personal` group has Expanded:false, but the Sidebar's
//     `expanded` useState initializer runs on first render BEFORE the async
//     /api/menu fetch resolves (groups=[] at that point), so every group
//     defaults open (`expanded[path] !== false` with an empty map). All 4
//     seeded sessions are therefore visible on load.
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

// Seed-derived expectations.
const ALL_TITLES = ['agent-deck', 'frontend', 'innotrade-api', 'scratch']
const RUNNING_TITLES = ['frontend']
const IDLE_TITLES = ['agent-deck', 'innotrade-api', 'scratch']

async function gotoSidebar(page) {
  await page.goto('/')
  // Sidebar list takes the initial /api/menu fetch + render to populate.
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

  test('group collapse hides member sessions; expand restores; no reload persistence', async ({ page }) => {
    const workHead = page.locator('[data-testid="group-head-work"]')

    // Collapse "work" → its 2 members (agent-deck, frontend) disappear.
    // work/innotrade is a distinct group path, so innotrade-api stays.
    await workHead.click()
    await expect(workHead.locator('.chev')).toHaveText('▸')
    await expect(page.locator('.sess')).toHaveCount(2)
    await expect(page.locator('.sess .tt')).toHaveText(['innotrade-api', 'scratch'])

    // Expand restores the members.
    await workHead.click()
    await expect(workHead.locator('.chev')).toHaveText('▾')
    await expect(page.locator('.sess')).toHaveCount(ALL_TITLES.length)

    // Collapse again, then reload: expansion is useState-only (audit:
    // Sidebar.js has no localStorage write for it), so it resets to open.
    await workHead.click()
    await expect(page.locator('.sess')).toHaveCount(2)
    await gotoSidebar(page)
    await expect(page.locator('.sess')).toHaveCount(ALL_TITLES.length)
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
