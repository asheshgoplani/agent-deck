// e2e/toasts.spec.js -- Toast notification system end-to-end coverage.
//
// Verifies the Toast.js behavior contract against the live fixture web
// server, driven through REAL UI flows (keyboard shortcuts that hit real
// API endpoints), not synthetic addToast calls:
//
//   - Error toast: Ctrl+Z with an empty undo stack → POST /api/sessions/
//     undelete returns 404 "nothing to undo"; apiFetch surfaces it as an
//     error toast (role="alert" group) that does NOT auto-dismiss, while
//     AppShell's companion "Nothing to undo" info toast (role="status")
//     auto-dismisses after ~5s.
//   - Success toast: delete a session via the API, then Ctrl+Z in the page
//     → "Restored session <id>" success toast auto-dismisses within ~5s.
//   - History drawer: dismissed toasts appear behind the topbar's
//     data-testid="toast-history-toggle" button, newest first.
//   - Stack cap: 4 rapid Ctrl+Z restores (4 success toasts) cap the
//     visible stack at 3 (oldest non-error evicted). Eviction ORDER
//     semantics are pinned by unit/toast.test.js; here we assert the visible
//     cap only. (These cases used the `r` "rename not implemented" info
//     toast as their source; `r` now opens the Edit dialog instead.)
//
// Viewport-agnostic: toasts are fixed-position overlays rendered on every
// viewport, keyboard shortcuts attach to `window`, and the history toggle
// lives in the always-visible topbar `.top-right` cluster — so all three
// projects (desktop / tablet / phone) run every test, no skips.

import { test, expect } from '@playwright/test'

async function gotoApp(page) {
  await page.goto('/')
  // Wait for the menu model to hydrate (one SSE roundtrip): keyboard
  // handlers like `r` need a focusable session. `.sess` rows exist in the
  // DOM on every viewport, but are not *visible* on the phone layout, so
  // wait for attachment, not visibility.
  await page.waitForSelector('.sess', { state: 'attached', timeout: 5000 })
}

test.describe('toast notifications', () => {
  test.beforeEach(async ({ page, request }) => {
    await request.post('/__fixture/reset')
    await gotoApp(page)
  })

  test('error toast from a failed mutation persists; companion info toast auto-dismisses', async ({ page }) => {
    // No delete since reset → undo stack empty → POST /api/sessions/undelete
    // 404s with message "nothing to undo". apiFetch toasts that message as an
    // error; AppShell's .catch adds the "Nothing to undo" info toast.
    await page.keyboard.press('Control+z')

    const errorToast = page.locator('[role="alert"] [data-testid="toast"]', { hasText: 'nothing to undo' })
    const infoToast = page.locator('[role="status"] [data-testid="toast"]', { hasText: 'Nothing to undo' })
    await expect(errorToast).toBeVisible({ timeout: 3000 })
    await expect(infoToast).toBeVisible({ timeout: 3000 })

    // Errors have no auto-dismiss timer (info/success expire at 5s) — after
    // 6s the error must still be on screen and the info toast must be gone.
    await page.waitForTimeout(6000)
    await expect(errorToast).toBeVisible()
    await expect(infoToast).toHaveCount(0)

    // Explicit dismissal is the only way to clear an error toast.
    await errorToast.locator('[data-testid="toast-dismiss"]').click()
    await expect(errorToast).toHaveCount(0)
  })

  test('success toast auto-dismisses within ~5s', async ({ page, request }) => {
    // Arrange a successful undo: delete sess-004 server-side, then Ctrl+Z
    // in the page restores it and toasts success.
    expect((await request.delete('/api/sessions/sess-004')).status()).toBe(200)
    await page.keyboard.press('Control+z')

    const successToast = page.locator('[role="status"] [data-testid="toast"]', { hasText: 'Restored session sess-004' })
    await expect(successToast).toBeVisible({ timeout: 3000 })
    // AUTO_DISMISS_MS is 5000; allow slack for the SPA timer to fire.
    await expect(successToast).toHaveCount(0, { timeout: 7000 })
  })

  test('dismissed toasts appear in the history drawer, newest first', async ({ page, request }) => {
    // Two distinct real toasts: delete two sessions server-side, then each
    // Ctrl+Z restores the most recent one and toasts "Restored session <id>".
    expect((await request.delete('/api/sessions/sess-003')).status()).toBe(200)
    expect((await request.delete('/api/sessions/sess-004')).status()).toBe(200)

    await page.keyboard.press('Control+z')
    const toastA = page.locator('[data-testid="toast"]', { hasText: 'Restored session sess-004' })
    await expect(toastA).toBeVisible({ timeout: 3000 })
    await toastA.locator('[data-testid="toast-dismiss"]').click()
    await expect(toastA).toHaveCount(0)

    await page.keyboard.press('Control+z')
    const toastB = page.locator('[data-testid="toast"]', { hasText: 'Restored session sess-003' })
    await expect(toastB).toBeVisible({ timeout: 3000 })
    await toastB.locator('[data-testid="toast-dismiss"]').click()
    await expect(toastB).toHaveCount(0)

    // Open the history drawer: B was dismissed last → rendered first.
    await page.locator('[data-testid="toast-history-toggle"]').click()
    await expect(page.locator('[data-testid="toast-history-drawer"]')).toBeVisible()
    const entries = page.locator('[data-testid="toast-history-entry"]')
    await expect(entries).toHaveCount(2)
    await expect(entries.nth(0)).toContainText('Restored session sess-003')
    await expect(entries.nth(1)).toContainText('Restored session sess-004')
    // Drawer metadata line shows the toast type.
    await expect(entries.nth(0)).toContainText('success')
  })

  test('expired (auto-dismissed) toasts also land in the history drawer', async ({ page, request }) => {
    expect((await request.delete('/api/sessions/sess-004')).status()).toBe(200)
    await page.keyboard.press('Control+z')
    const toast = page.locator('[data-testid="toast"]', { hasText: 'Restored session sess-004' })
    await expect(toast).toBeVisible({ timeout: 3000 })
    // Don't touch it — let the 5s auto-dismiss timer expire it.
    await expect(toast).toHaveCount(0, { timeout: 7000 })

    await page.locator('[data-testid="toast-history-toggle"]').click()
    const entries = page.locator('[data-testid="toast-history-entry"]')
    await expect(entries).toHaveCount(1)
    await expect(entries.first()).toContainText('Restored session sess-004')
  })

  test('visible toast stack caps at 3', async ({ page, request }) => {
    // 4 deletes, then 4 rapid Ctrl+Z restores create 4 success toasts through
    // the real keyboard path; Toast.js evicts the oldest non-error so only 3
    // ever render. (Exact eviction-order semantics are pinned in
    // unit/toast.test.js.)
    for (const id of ['sess-001', 'sess-002', 'sess-003', 'sess-004']) {
      expect((await request.delete(`/api/sessions/${id}`)).status()).toBe(200)
    }
    for (let i = 0; i < 4; i++) {
      await page.keyboard.press('Control+z')
      await expect(page.locator('[data-testid="toast"]', { hasText: 'Restored session' }).first()).toBeVisible({ timeout: 3000 })
    }
    await expect(page.locator('[data-testid="toast"]', { hasText: 'Restored session sess-001' })).toBeVisible()
    await expect(page.locator('[data-testid="toast"]')).toHaveCount(3)
  })
})
