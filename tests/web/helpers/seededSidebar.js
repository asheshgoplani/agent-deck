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
// "collapsed groups render open anyway".
//
// CALL THIS BEFORE page.goto(). It opens the group server-side, so the page's
// very first /api/menu fetch already reports it expanded. Both alternatives
// break some caller:
//   - clicking the chevron after load cannot work on the phone project, where
//     the sidebar is laid out but not shown (service-worker-recovery runs on
//     every viewport);
//   - PATCHing after load relies on the SSE snapshot to tell the client, and
//     some specs deliberately abort /events/menu (session-actions-ui's Fork
//     test) so a doctored /api/menu payload can't be overwritten.
// The chevron click itself is covered where it belongs, by the collapse tests
// in group-selection.spec.js and sidebar-chrome.spec.js.
import { expect } from '@playwright/test'

/** Groups the fixture seeds with Expanded:false. */
const SEEDED_COLLAPSED = ['personal']

/**
 * Open every group the fixture seeds collapsed, via the same endpoint the
 * sidebar's chevron writes through. Idempotent. Run after /__fixture/reset
 * (which re-collapses them) and before navigating; the caller's own row-count
 * assertion then waits for the render.
 */
export async function expandSeededCollapsedGroups(page) {
  for (const path of SEEDED_COLLAPSED) {
    const res = await page.request.patch(`/api/groups/${encodeURIComponent(path)}`, {
      data: { expanded: true },
    })
    expect(res.ok(), `PATCH /api/groups/${path} {expanded:true} -> ${res.status()}`).toBeTruthy()
  }
}
