// e2e/sidebar-groups.spec.js -- regression coverage for sidebar group
// behaviors that v1.8 shipped broken (CRIT-01, UX-05) and that TEST-PLAN.md
// §2.B requires before v1.9 ships.
//
// Cells covered:
//   B8 — collapsed top-level group MUST remain visible. Only its children
//        hide. See SessionList.js:hasCollapsedStrictAncestor (the fix that
//        closes BUG #1 / CRIT-01).
//   B10 / J7 — group child count reflects what is actually rendered
//        (countVisibleChildren), not the static server-side sessionCount.
//        With an active search the displayed count drops to the matching
//        count. See GroupRow.js:23-44 (the fix that closes BUG #16 / UX-05).

import { test, expect } from '@playwright/test'

// Sidebar is `lg:translate-x-0` — always-visible at ≥1024px viewports. On
// tablet (820) and phone (393) the sidebar is overlay-hidden by default,
// which is orthogonal to the group-collapse behaviors under test here.
// Scope these specs to the desktop project; responsive behaviors get their
// own dedicated coverage in §2.M.
test.beforeEach(({}, testInfo) => {
  test.skip(
    testInfo.project.name !== 'chromium-desktop',
    'desktop-only: sidebar overlay is closed by default at tablet/phone viewports',
  )
})

async function resetFixture(request) {
  const res = await request.post('/__fixture/reset')
  expect(res.status()).toBe(204)
}

async function gotoFreshApp(page) {
  // Force a fully clean app state for the first navigation only. We can't
  // use page.addInitScript because that fires on every reload, which would
  // wipe localStorage in the middle of the persistence test below. Instead,
  // navigate to a blank doc on the test origin, clear, then go to '/'.
  await page.goto('/healthz') // any same-origin URL works; healthz returns JSON
  await page.evaluate(() => {
    try { localStorage.clear() } catch (_) {}
  })
  await page.goto('/')
  await page.waitForFunction(() => window.__preactSessionListActive === true, {
    timeout: 5000,
  })
}

// Scope every sidebar-only locator under <aside> so the "Recently active"
// dashboard in <main> (EmptyStateDashboard.js, which also renders
// data-session-id buttons) doesn't fight us with strict-mode duplicate hits.
function aside(page) {
  return page.locator('aside')
}

function groupRow(page, name) {
  // GroupRow.js renders a <button aria-expanded> whose accessible name is
  // assembled from all descendant text: e.g. "▾ work (3) Create subgroup
  // Rename group Delete group". Use a CSS+hasText filter on the inner span
  // whose `title` attribute carries the bare group name — that's the only
  // unambiguous handle on a row.
  return aside(page)
    .locator('button[aria-expanded]')
    .filter({ has: page.locator(`span[title="${name}"]`) })
}

function sessionRow(page, sessionId) {
  return aside(page).locator(`[data-session-id="${sessionId}"]`)
}

test.describe('sidebar — group collapse (B8, REGRESSION CRIT-01)', () => {
  test.beforeEach(async ({ request }) => {
    await resetFixture(request)
  })

  test('collapsing a top-level group hides its children but keeps the group row visible', async ({
    page,
  }) => {
    await gotoFreshApp(page)

    const work = groupRow(page, 'work')
    await expect(work, 'work group row should render').toBeVisible()
    await expect(work).toHaveAttribute('aria-expanded', 'true')

    // The fixture seeds sess-001 and sess-002 directly under "work".
    await expect(sessionRow(page, 'sess-001')).toBeVisible()
    await expect(sessionRow(page, 'sess-002')).toBeVisible()

    // Collapse "work".
    await work.click()

    // CRIT-01 invariant: the group row itself MUST still render. Pre-fix,
    // hasCollapsedAncestor(group.path) returned true for the row's own
    // path and filtered the row out of the visible[] array in SessionList.
    await expect(
      work,
      'CRIT-01: collapsed top-level group row must remain visible (BUG #1)',
    ).toBeVisible()
    await expect(work).toHaveAttribute('aria-expanded', 'false')

    // Children are hidden.
    await expect(sessionRow(page, 'sess-001')).toHaveCount(0)
    await expect(sessionRow(page, 'sess-002')).toHaveCount(0)

    // Expanding restores children without a page reload (signal-driven).
    await work.click()
    await expect(work).toHaveAttribute('aria-expanded', 'true')
    await expect(sessionRow(page, 'sess-001')).toBeVisible()
    await expect(sessionRow(page, 'sess-002')).toBeVisible()
  })

  test('collapsing a parent hides nested subgroup AND its descendants, but parent stays visible', async ({
    page,
  }) => {
    await gotoFreshApp(page)

    const work = groupRow(page, 'work')
    const innotrade = groupRow(page, 'innotrade')

    await expect(work).toBeVisible()
    await expect(innotrade).toBeVisible()
    await expect(sessionRow(page, 'sess-003')).toBeVisible()

    // Collapse the parent "work" — innotrade and sess-003 must vanish,
    // but the "work" row itself must stay (the recursion uses strict
    // ancestor for groups, full ancestor for sessions).
    await work.click()
    await expect(work).toBeVisible()
    await expect(work).toHaveAttribute('aria-expanded', 'false')
    await expect(innotrade).toHaveCount(0)
    await expect(sessionRow(page, 'sess-003')).toHaveCount(0)
  })

  test('group collapse state persists across reload via localStorage', async ({ page }) => {
    await gotoFreshApp(page)
    const work = groupRow(page, 'work')
    await work.click() // collapse
    await expect(work).toHaveAttribute('aria-expanded', 'false')

    // Reload — state must restore from localStorage agentdeck.groupExpanded.
    await page.reload()
    await page.waitForFunction(() => window.__preactSessionListActive === true, {
      timeout: 5000,
    })
    const workAfter = groupRow(page, 'work')
    await expect(workAfter).toBeVisible()
    await expect(workAfter).toHaveAttribute('aria-expanded', 'false')
    await expect(sessionRow(page, 'sess-001')).toHaveCount(0)
  })
})

test.describe('sidebar — group child count (B10/J7, REGRESSION UX-05)', () => {
  test.beforeEach(async ({ request }) => {
    await resetFixture(request)
  })

  test('group count reflects countVisibleChildren, not server static sessionCount', async ({
    page,
  }) => {
    await gotoFreshApp(page)

    const work = groupRow(page, 'work')
    await expect(work).toBeVisible()
    // The "work" group owns sess-001 + sess-002 directly. The countVisible
    // implementation also walks subgroups (sess-003 in work/innotrade), so
    // the displayed count is 3, not the server-static `sessionCount: 2`.
    // The displayed count is the recursive count of visible descendants.
    await expect(
      work,
      'work group label must include a numeric count in parentheses',
    ).toContainText(/\(\d+\)/)

    // Verify the count is 3 (sess-001, sess-002, sess-003 under work/*).
    const text = (await work.textContent()) || ''
    const match = text.match(/\((\d+)\)/)
    expect(match, `expected "(N)" in "${text}"`).not.toBeNull()
    expect(Number(match[1])).toBe(3)
  })

  test('typing a query narrows the displayed count to matching children only', async ({
    page,
  }) => {
    await gotoFreshApp(page)

    // Open the search by clicking its collapsed-state button. Using the
    // visible affordance instead of the `/` shortcut keeps this test focused
    // on the count-derivation behavior; the shortcut itself is covered by
    // B2 in §2.B.
    await aside(page).getByRole('button', { name: /Filter sessions/i }).click()

    const input = aside(page).locator('input[placeholder="Filter sessions..."]')
    await expect(input).toBeFocused()

    // Type a term that matches sess-002 only (its title is "frontend"),
    // which lives directly under "work".
    await input.fill('frontend')

    // useDebounced is 250ms; the GroupRow count reads the raw signal so it
    // updates immediately, but allow a frame for Preact to re-render.
    await page.waitForTimeout(100)

    const work = groupRow(page, 'work')
    await expect(work).toBeVisible()
    const text = (await work.textContent()) || ''
    const match = text.match(/\((\d+)\)/)
    expect(match, `expected "(N)" in filtered "${text}"`).not.toBeNull()
    expect(
      Number(match[1]),
      'UX-05: filtered group count must shrink to the count of matching descendants',
    ).toBe(1)
  })
})
