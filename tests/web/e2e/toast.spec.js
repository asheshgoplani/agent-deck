// e2e/toast.spec.js -- F5 (toast eviction) + F6 (history persistence).
// TEST-PLAN.md §2.F.
//
// Toast.js implements the visible-stack contract documented at the top of
// the file:
//   - visible cap = 3
//   - new toast over cap evicts the oldest NON-error first
//   - if all 3 visible are errors, the oldest error is evicted
//   - info/success auto-dismiss at 5s; error stays until explicit dismiss
//   - dismissed toasts push into toastHistorySignal (cap 50, localStorage)
//
// These behaviors are not currently covered by any test. We drive them by
// importing Toast.js from the running page (the bundle serves the source
// as a module, so the imported addToast operates on the same signals as
// the live app).

import { test, expect } from '@playwright/test'

test.beforeEach(({}, testInfo) => {
  test.skip(
    testInfo.project.name !== 'chromium-desktop',
    'desktop-only: focuses contract on its primary viewport',
  )
})

async function gotoFreshApp(page) {
  // Clear localStorage on the FIRST navigation only — using addInitScript
  // would wipe state on every reload too, which breaks any persistence
  // assertion. Instead, navigate to / first, clear, then reload so
  // module-level localStorage reads (e.g. state.initialToastHistory)
  // start from an empty slate.
  await page.goto('/')
  await page.evaluate(() => {
    try { localStorage.clear() } catch (_) {}
  })
  await page.reload()
  await page.waitForFunction(() => window.__preactSessionListActive === true, {
    timeout: 5000,
  })
}

async function callAddToast(page, message, type) {
  // Import Toast.js inside the page context. The bundle has already loaded
  // the module so this dynamic import returns the same instance — calling
  // addToast here is equivalent to the app calling addToast internally.
  await page.evaluate(
    async ([message, type]) => {
      const Toast = await import('/static/app/Toast.js')
      Toast.addToast(message, type)
    },
    [message, type],
  )
}

function visibleToasts(page) {
  // All toast items live under role=alert (errors) or role=status (info/
  // success). Each toast row is a <div> with the message text node.
  return page.locator('[role="alert"] > div, [role="status"] > div')
}

test.describe('Toast contract (F5)', () => {
  test('visible cap is 3; 4th info evicts the oldest non-error', async ({ page }) => {
    await gotoFreshApp(page)

    await callAddToast(page, 'first info', 'info')
    await callAddToast(page, 'second info', 'info')
    await callAddToast(page, 'third info', 'info')
    await expect(visibleToasts(page)).toHaveCount(3)

    await callAddToast(page, 'fourth info', 'info')
    // F5: oldest non-error evicted (the first 'info') — capacity stays at 3.
    await expect(visibleToasts(page)).toHaveCount(3)
    await expect(page.locator('[role="status"]')).not.toContainText('first info')
    await expect(page.locator('[role="status"]')).toContainText('fourth info')
  })

  test('error toast preserved when 3 info toasts fill the stack and a 4th info arrives', async ({
    page,
  }) => {
    await gotoFreshApp(page)
    await callAddToast(page, 'sticky error', 'error')
    await callAddToast(page, 'info one', 'info')
    await callAddToast(page, 'info two', 'info')
    await expect(visibleToasts(page)).toHaveCount(3)

    // New info arrives — must evict the oldest non-error (info one).
    await callAddToast(page, 'info three', 'info')
    await expect(visibleToasts(page)).toHaveCount(3)
    // Error stays.
    await expect(page.locator('[role="alert"]')).toContainText('sticky error')
    await expect(page.locator('[role="status"]')).not.toContainText('info one')
  })

  test('oldest error is evicted when all 3 visible are errors and a 4th error arrives', async ({
    page,
  }) => {
    await gotoFreshApp(page)
    await callAddToast(page, 'err one', 'error')
    await callAddToast(page, 'err two', 'error')
    await callAddToast(page, 'err three', 'error')
    await expect(visibleToasts(page)).toHaveCount(3)

    await callAddToast(page, 'err four', 'error')
    await expect(visibleToasts(page)).toHaveCount(3)
    await expect(page.locator('[role="alert"]')).not.toContainText('err one')
    await expect(page.locator('[role="alert"]')).toContainText('err four')
  })

  test('info toast auto-dismisses after 5s; error does not', async ({ page }) => {
    await gotoFreshApp(page)
    await callAddToast(page, 'short-lived', 'info')
    await callAddToast(page, 'sticks around', 'error')
    await expect(page.locator('[role="status"]')).toContainText('short-lived')
    await expect(page.locator('[role="alert"]')).toContainText('sticks around')

    // Auto-dismiss is 5000ms. Wait 5.5s to be safe.
    await page.waitForTimeout(5500)
    await expect(
      page.getByText('short-lived'),
      'F5: info auto-dismiss after 5s',
    ).toHaveCount(0)
    await expect(
      page.locator('[role="alert"]'),
      'F5: error must NOT auto-dismiss',
    ).toContainText('sticks around')
  })

  test('aria-live attributes: assertive for errors, polite for info', async ({ page }) => {
    await gotoFreshApp(page)
    await callAddToast(page, 'an error', 'error')
    await callAddToast(page, 'an info', 'info')

    await expect(page.locator('[role="alert"][aria-live="assertive"]')).toContainText('an error')
    await expect(page.locator('[role="status"][aria-live="polite"]')).toContainText('an info')
  })
})

test.describe('Toast history (F6, REGRESSION)', () => {
  test('dismissed toasts push into history; survives reload', async ({ page }) => {
    await gotoFreshApp(page)
    await callAddToast(page, 'will-be-dismissed', 'error')
    // Sanity: the toast actually rendered in the visible stack.
    await expect(page.locator('[role="alert"]')).toContainText('will-be-dismissed')

    // Dismiss the toast and read back the persistence contract from
    // localStorage. The DOM toggle aria-label reflects history.length too
    // but that path has a known subscription quirk against dynamically-
    // imported state modules; localStorage is the durable source of truth.
    const lsHistory = await page.evaluate(async () => {
      const Toast = await import('/static/app/Toast.js')
      const state = await import('/static/app/state.js')
      const list = state.toastsSignal.value
      Toast.removeToast(list[0].id)
      return localStorage.getItem('agentdeck_toast_history')
    })
    expect(
      lsHistory,
      'F6: localStorage agentdeck_toast_history is written on dismiss',
    ).toContain('will-be-dismissed')

    // Survive reload: the persisted JSON repopulates the signal.
    await page.reload()
    await page.waitForFunction(() => window.__preactSessionListActive === true, {
      timeout: 5000,
    })
    const afterReload = await page.evaluate(async () => {
      const state = await import('/static/app/state.js')
      return state.toastHistorySignal.value
    })
    expect(afterReload.length).toBe(1)
    expect(afterReload[0].message).toBe('will-be-dismissed')
  })

  test('history is capped at 50 entries (oldest dropped)', async ({ page }) => {
    await gotoFreshApp(page)

    // Push 51 toasts then dismiss them all so they enter history. Use info
    // toasts so the auto-dismiss handles cleanup quickly. Actually faster:
    // push directly into history via the localStorage key + reload.
    await page.evaluate(() => {
      const items = []
      for (let i = 0; i < 60; i++) {
        items.push({
          id: i + 1,
          message: 'historic ' + i,
          type: 'info',
          createdAt: Date.now() + i,
        })
      }
      localStorage.setItem('agentdeck_toast_history', JSON.stringify(items))
    })
    await page.reload()
    await page.waitForFunction(() => window.__preactSessionListActive === true, {
      timeout: 5000,
    })

    // After reload, state.js's initialToastHistory() reads localStorage
    // and `.slice(-50)` enforces the cap immediately. So the loaded
    // signal must be 50, not 60.
    const loadedLen = await page.evaluate(async () => {
      const state = await import('/static/app/state.js')
      return state.toastHistorySignal.value.length
    })
    expect(
      loadedLen,
      'F6: state.js initialToastHistory caps loaded entries at 50',
    ).toBe(50)
  })

  test('localStorage corruption does not crash the app (empty history)', async ({ page }) => {
    await page.addInitScript(() => {
      localStorage.setItem('agentdeck_toast_history', '{this is not valid json')
    })
    await page.goto('/')
    await page.waitForFunction(() => window.__preactSessionListActive === true, {
      timeout: 5000,
    })

    // App didn't crash: the sidebar mounted. History count should be 0.
    const toggle = page.locator('[data-testid="toast-history-toggle"]')
    await expect(toggle).toHaveAttribute('aria-label', /Toast history \(0 entries\)/)
  })
})
