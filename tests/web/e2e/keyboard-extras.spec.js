// e2e/keyboard-extras.spec.js — keyboard bindings NOT covered by
// keyboard-parity.spec.js (which owns / ? j k Enter Shift+Enter n r Shift+D q
// Esc + the typing guard).
//
// Covered here (grounded in AppShell.js onKey):
//   - `]`        → toggles railSignal visible/hidden (body[data-rail],
//                  .rightrail display, localStorage agentdeck.rail).
//   - Ctrl/Cmd+Z → POST /api/sessions/undelete. The handler accepts EITHER
//                  modifier (`e.metaKey || e.ctrlKey`), so it works on
//                  mac (Cmd) and linux/windows (Ctrl) alike. Success toast:
//                  "Restored session {id}"; empty-stack toast: "Nothing to
//                  undo" (apiFetch rejects on the fixture's 404).
//
// NOT here, by design:
//   - Ctrl/Cmd+K → command-palette.spec.js owns it.
//   - The undelete API contract (LIFO, 404, window) → close-undo.spec.js owns
//     it; this file only proves the KEYBOARD path drives that API from a
//     pointer-driven delete.
//   - `T` for tweaks: documented in some places but NOT implemented in
//     AppShell.js onKey — nothing to test until the binding exists.
//
// Same viewport policy as keyboard-parity.spec.js: keyboard flows assert
// against sidebar rows / the right rail, both display:none on phones, so the
// whole suite is pinned to viewports ≥ 768px.

import { test, expect } from '@playwright/test'

async function waitForAppMount(page) {
  await page.waitForFunction(() => {
    const root = document.querySelector('#app, .app, [data-testid="app-root"], main')
    return root && root.textContent && root.textContent.trim().length > 50
  }, { timeout: 5000 })
  // Sidebar list takes one SSE roundtrip to populate after mount.
  await page.waitForSelector('.sess', { timeout: 5000 })
}

test.describe('keyboard extras', () => {
  test.skip(({ viewport }) => (viewport?.width || 1280) < 768, 'phone viewport: keyboard bindings act on sidebar/rail, hidden on phones')

  test.beforeEach(async ({ page, request }) => {
    await request.post('/__fixture/reset')
    await page.goto('/')
    await waitForAppMount(page)
    // Defensive: blur whatever may have stolen focus on mount, so the
    // in-field guard in AppShell.onKey doesn't swallow the keypress.
    await page.evaluate(() => document.activeElement && document.activeElement.blur && document.activeElement.blur())
  })

  test('] toggles the right rail and persists to localStorage', async ({ page }) => {
    const rail = page.locator('[data-testid="right-rail"]')
    await expect(rail).toBeVisible()
    await expect(page.locator('body')).toHaveAttribute('data-rail', 'visible')

    await page.keyboard.press(']')
    await expect(rail).toBeHidden()
    await expect(page.locator('body')).toHaveAttribute('data-rail', 'hidden')
    // uiState persist() JSON-encodes the railSignal value.
    expect(await page.evaluate(() => localStorage.getItem('agentdeck.rail'))).toBe('"hidden"')

    await page.keyboard.press(']')
    await expect(rail).toBeVisible()
    await expect(page.locator('body')).toHaveAttribute('data-rail', 'visible')
    expect(await page.evaluate(() => localStorage.getItem('agentdeck.rail'))).toBe('"visible"')
  })

  test('Ctrl+Z after a UI-driven delete restores the session row', async ({ page }) => {
    // Delete sess-002 ("frontend", group work, expanded in the seed) through
    // the sidebar row's danger button. Row actions are display:none until
    // :hover (app.css .sess:hover .actions), so hover first.
    const row = page.locator('.sess', { hasText: 'frontend' })
    await expect(row).toBeVisible()
    await row.hover()
    await row.locator('button[title="Delete"]').click()

    // ConfirmDialog: kicker CONFIRM, message "Delete session …", confirm
    // button labelled "Delete" (ConfirmDialog.js).
    const dialog = page.locator('[role="dialog"]', { hasText: /delete session/i })
    await expect(dialog).toBeVisible()
    await dialog.getByRole('button', { name: 'Delete' }).click()

    // SSE menu refresh removes the row.
    await expect(page.locator('.sess .tt', { hasText: 'frontend' })).toHaveCount(0, { timeout: 4000 })

    // Ctrl+Z drives POST /api/sessions/undelete (AppShell.onKey).
    await page.keyboard.press('Control+z')
    await expect(page.locator('.toast', { hasText: /Restored session sess-002/ })).toBeVisible({ timeout: 2000 })
    await expect(page.locator('.sess .tt', { hasText: 'frontend' })).toBeVisible({ timeout: 4000 })
  })

  test('Ctrl+Z with nothing to undo shows the "Nothing to undo" toast', async ({ page }) => {
    // Fresh fixture reset → undo stack is empty → fixture returns 404 →
    // apiFetch rejects → AppShell catch toasts "Nothing to undo" (info).
    await page.keyboard.press('Control+z')
    // Scope to the role="status" group: the failing POST also surfaces an
    // apiFetch error toast ("nothing to undo") in the role="alert" group,
    // and hasText matches case-insensitively — unscoped `.toast` hits both.
    await expect(page.locator('[role="status"] .toast', { hasText: 'Nothing to undo' })).toBeVisible({ timeout: 2000 })
  })

  test('Cmd+Z (Meta) is accepted as the undo modifier too', async ({ page }) => {
    // The handler checks `e.metaKey || e.ctrlKey`, so the mac-style chord
    // must reach the same code path. Empty stack → same info toast.
    await page.keyboard.press('Meta+z')
    await expect(page.locator('[role="status"] .toast', { hasText: 'Nothing to undo' })).toBeVisible({ timeout: 2000 })
  })
})
