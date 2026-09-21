// Shared setup for e2e specs that assert on the FULL seeded session list.
//
// The fixture seeds `personal` with `Expanded: false`
// (tests/web/fixtures/cmd/web-fixture/main.go seed()). The sidebar used to
// ignore that flag outright — there was no way to write it back from the
// browser, so honoring it would have leaked TUI collapse state one-way — and
// every group therefore rendered open, putting all four seeded sessions on
// screen for free.
//
// PATCH /api/groups/{path} {expanded} closed that loop, so the client now
// adopts `MenuGroup.Expanded` from each snapshot and `personal` renders
// collapsed on a fresh load, hiding `scratch` (sess-004).
//
// Specs that need all four rows now say so explicitly instead of relying on
// "collapsed groups render open anyway". Expanding through the chevron — a
// real user action that round-trips the new endpoint — is deliberate: it keeps
// these specs honest about the seeded state rather than pretending the group
// was never collapsed.
import { expect } from '@playwright/test'

/** Chevron glyph the sidebar renders for a collapsed group (Sidebar.js). */
const COLLAPSED = '▸'

/**
 * Expand every group the fixture seeds collapsed, so the full seeded session
 * set is visible. Safe to call when the group is already open.
 */
export async function expandSeededCollapsedGroups(page) {
  const chevron = page.locator('[data-testid="group-chev-personal"]')
  await chevron.waitFor({ state: 'visible', timeout: 5000 })

  if ((await chevron.textContent())?.trim() === COLLAPSED) {
    await chevron.click()
    // The click PATCHes the server and waits for the snapshot to come back, so
    // assert the row actually arrived rather than racing the next assertion.
    await expect(page.locator('.sess .tt', { hasText: 'scratch' })).toHaveCount(1, { timeout: 5000 })
  }
}
