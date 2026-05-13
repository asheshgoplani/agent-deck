// e2e/sidebar-status.spec.js -- B14 (REGRESSION v1.8 status divergence).
//
// SessionRow.STATUS_COLORS maps each session.status value to a CSS class
// stack that determines the dot color and pulse animation. v1.8 shipped a
// regression where the TUI hookStatus and web status disagreed; the visible
// symptom was a green dot on a stopped session (or vice versa). The cells
// below drive every status value through the fixture's __fixture/session/
// {id}/status endpoint, then assert the dot class in the DOM matches.
//
// The fixture endpoint runs the transition entirely in process state, so
// the only path from "status changed" to "dot updates" is the SSE
// snapshot. That's exactly the wiring we want to pin.

import { test, expect } from '@playwright/test'

test.beforeEach(({}, testInfo) => {
  test.skip(
    testInfo.project.name !== 'chromium-desktop',
    'desktop-only: sidebar overlay is closed by default at tablet/phone viewports',
  )
})

async function gotoFreshApp(page) {
  await page.goto('/healthz')
  await page.evaluate(() => {
    try { localStorage.clear() } catch (_) {}
  })
  await page.goto('/')
  await page.waitForFunction(() => window.__preactSessionListActive === true, {
    timeout: 5000,
  })
}

async function resetFixture(request) {
  const res = await request.post('/__fixture/reset')
  expect(res.status()).toBe(204)
}

function sidebarSession(page, sessionId) {
  return page.locator('aside').locator(`[data-session-id="${sessionId}"]`)
}

async function dotClass(page, sessionId) {
  // The dot is the first <span class="rounded-full"> inside the session row.
  // It carries every status-related class: bg-tn-{green|yellow|...} and
  // animate-pulse when applicable.
  const dot = sidebarSession(page, sessionId).locator('span.rounded-full').first()
  return (await dot.getAttribute('class')) || ''
}

// Drive sess-002 (runs as `running` in the seed) through every status the
// matrix promises a color for. The fixture endpoint mutates state in-process
// and triggers SSE on the next /events/menu poll tick (≤2s).
const STATUS_EXPECT = [
  { to: 'running', cls: /bg-tn-green/, pulse: true },
  { to: 'waiting', cls: /bg-tn-yellow/, pulse: true },
  { to: 'starting', cls: /bg-tn-purple/, pulse: true },
  { to: 'idle', cls: /bg-tn-muted(?!\/)/, pulse: false },
  { to: 'error', cls: /bg-tn-red/, pulse: false },
  { to: 'stopped', cls: /bg-tn-muted\/50/, pulse: false },
]

test.describe('sidebar — session status dot mapping (B14, REGRESSION v1.8)', () => {
  test.beforeEach(async ({ request }) => {
    await resetFixture(request)
  })

  for (const { to, cls, pulse } of STATUS_EXPECT) {
    test(`status="${to}" → dot has ${cls} and animate-pulse=${pulse}`, async ({
      page,
      request,
    }) => {
      await gotoFreshApp(page)
      await expect(sidebarSession(page, 'sess-002')).toBeVisible()

      // Force the transition. The fixture handler is synchronous; the SSE
      // delivery follows the next /events/menu poll tick (≤2s).
      const res = await request.post(
        `/__fixture/session/sess-002/status?to=${to}`,
      )
      expect(res.status()).toBe(204)

      // Wait for the dot class to reflect the new status. Use a generous
      // timeout to cover the SSE poll interval; SSE delivery typically
      // arrives well under 2s.
      await expect
        .poll(
          () => dotClass(page, 'sess-002'),
          { timeout: 5000, intervals: [100, 200, 400, 800] },
        )
        .toMatch(cls)

      const className = await dotClass(page, 'sess-002')
      if (pulse) {
        expect(className, `status=${to} should pulse`).toMatch(/animate-pulse/)
      } else {
        expect(className, `status=${to} should NOT pulse`).not.toMatch(/animate-pulse/)
      }
    })
  }

  test('rapid status flip (running → error → stopped) settles on the final value', async ({
    page,
    request,
  }) => {
    await gotoFreshApp(page)
    await expect(sidebarSession(page, 'sess-002')).toBeVisible()

    // Multiple back-to-back transitions exercise the SSE fingerprint dedupe
    // path (handlers_events.go:126-148) — only the final state should be
    // reflected after all snapshots are processed.
    for (const to of ['running', 'error', 'stopped']) {
      const res = await request.post(`/__fixture/session/sess-002/status?to=${to}`)
      expect(res.status()).toBe(204)
    }

    await expect
      .poll(() => dotClass(page, 'sess-002'), { timeout: 5000 })
      .toMatch(/bg-tn-muted\/50/)
  })
})
