// e2e/url-routing.spec.js -- URL <-> selection sync (App.js phase 6).
//
// Covers the /s/{id} route contract:
//   - Deep link: booting at /s/sess-001 selects that session
//     (main.js applyRouteSelection() parses the path before first render).
//   - Selecting a session in the sidebar pushes /s/{id} onto history.
//   - Browser back/forward replays selection via App.js's popstate handler
//     (back to / clears the selection entirely).
//   - An unknown id in the URL must not crash the app: the app mounts, no
//     sidebar row is selected (the right rail falls back to the first
//     session by design — RightRail.js `find(...) || sessions[0]`).
//   - Shift+Enter's new tab actually loads with the session selected. The
//     new-tab *event* itself (URL carries #session=) is already covered by
//     keyboard-parity.spec.js; here we assert the loaded state of the opened
//     tab, which selects via the /s/{id} path it inherits.

import { test, expect } from '@playwright/test'

async function waitForAppMount(page) {
  await page.waitForFunction(() => {
    const root = document.querySelector('#app, .app, [data-testid="app-root"], main')
    return root && root.textContent && root.textContent.trim().length > 50
  }, { timeout: 5000 })
  // Sidebar list takes one SSE roundtrip to populate after mount.
  await page.waitForSelector('.sess', { timeout: 5000 })
}

// Selection is asserted through the sidebar `.sess.sel` row, which is
// display:none below 720px (phone uses the touch-first bottom-tab layout).
test.describe('URL routing (/s/{id})', () => {
  test.skip(({ viewport }) => (viewport?.width || 1280) < 768, 'phone viewport: selection assertions rely on the sidebar (desktop/tablet only)')

  test.beforeEach(async ({ request }) => {
    await request.post('/__fixture/reset')
  })

  test('deep link /s/sess-001 loads with that session selected', async ({ page }) => {
    await page.goto('/s/sess-001')
    await waitForAppMount(page)
    await expect(page.locator('.sess.sel .tt')).toHaveText('agent-deck')
    // The URL is not rewritten on boot.
    expect(new URL(page.url()).pathname).toBe('/s/sess-001')
  })

  test('selecting a session pushes /s/{id} onto the URL', async ({ page }) => {
    await page.goto('/')
    await waitForAppMount(page)
    await page.locator('.sess .tt', { hasText: 'frontend' }).click()
    await expect(page.locator('.sess.sel .tt')).toHaveText('frontend')
    await expect.poll(() => new URL(page.url()).pathname).toBe('/s/sess-002')
  })

  test('browser back/forward replays selection history', async ({ page }) => {
    await page.goto('/')
    await waitForAppMount(page)

    // Build history: / -> /s/sess-001 -> /s/sess-002.
    await page.locator('.sess .tt', { hasText: 'agent-deck' }).click()
    await expect.poll(() => new URL(page.url()).pathname).toBe('/s/sess-001')
    await page.locator('.sess .tt', { hasText: 'frontend' }).click()
    await expect.poll(() => new URL(page.url()).pathname).toBe('/s/sess-002')

    // Back: restores the previous selection.
    await page.goBack()
    await expect.poll(() => new URL(page.url()).pathname).toBe('/s/sess-001')
    await expect(page.locator('.sess.sel .tt')).toHaveText('agent-deck')

    // Back to /: clears the selection (App.js popstate handler nulls it).
    await page.goBack()
    await expect.poll(() => new URL(page.url()).pathname).toBe('/')
    await expect(page.locator('.sess.sel')).toHaveCount(0)

    // Forward: re-selects sess-001.
    await page.goForward()
    await expect.poll(() => new URL(page.url()).pathname).toBe('/s/sess-001')
    await expect(page.locator('.sess.sel .tt')).toHaveText('agent-deck')
  })

  test('unknown session id in URL does not crash the app', async ({ page }) => {
    await page.goto('/s/does-not-exist')
    await waitForAppMount(page)
    // App mounted with the seeded sidebar, but nothing is selected — no row
    // matches the bogus id. (Assert by title, not row count: the "personal"
    // group's expansion state would make an exact count race-dependent.)
    await expect(page.locator('.sess .tt', { hasText: 'agent-deck' })).toBeVisible()
    await expect(page.locator('.sess .tt', { hasText: 'frontend' })).toBeVisible()
    await expect(page.locator('.sess.sel')).toHaveCount(0)
    // Shell chrome rendered fine (no blank/error page).
    await expect(page.locator('.topbar')).toBeVisible()
  })

  test('Shift+Enter new tab loads with the session selected', async ({ page, context }) => {
    // Select sess-002 via deep link so the tab stays on fleet (no xterm
    // focus-steal) and window.location.pathname is /s/sess-002 when
    // AppShell builds the new-tab URL.
    await page.goto('/s/sess-002')
    await waitForAppMount(page)
    await expect(page.locator('.sess.sel .tt')).toHaveText('frontend')

    // Defensive: blur anything that may have stolen focus on mount.
    await page.evaluate(() => document.activeElement && document.activeElement.blur && document.activeElement.blur())
    const pagePromise = context.waitForEvent('page', { timeout: 3000 })
    await page.keyboard.press('Shift+Enter')
    const newPage = await pagePromise

    // The opened URL carries both the /s/ path and the #session= fragment.
    expect(new URL(newPage.url()).pathname).toBe('/s/sess-002')
    expect(newPage.url()).toContain('#session=sess-002')

    // And the loaded tab actually has the session selected (via the path).
    await waitForAppMount(newPage)
    await expect(newPage.locator('.sess.sel .tt')).toHaveText('frontend')
    await newPage.close()
  })
})
