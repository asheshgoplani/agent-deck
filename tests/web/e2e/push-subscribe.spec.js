// e2e/push-subscribe.spec.js — browser push subscription flow (issue #2413).
//
// `agent-deck web --push` serves /api/push/* but the rewritten UI never
// subscribed the browser. These tests drive the real client code in
// static/app/push.js through the Tweaks panel "NOTIFICATIONS" switch.
//
// Headless Chromium has no push service to hand out real endpoints, and the
// fixture binary has no push service (no VAPID keys), so the test fakes the
// two ends it cannot own: the browser's pushManager / Notification permission
// (init script, state kept in localStorage so a reload sees the same
// subscription, like a real browser) and the /api/push/* responses
// (page.route, which also records what the client posted).

import { test, expect } from '@playwright/test'

// 65-byte uncompressed P-256 point, base64url, the shape --push serves.
const VAPID_KEY = 'BNcRdreALRFXTkOOUHK1EtK2wtaz5Ry4YfYCA_0QTpQtUbVlUls0VJXg7A8u-Ts1XbjhazAkj7I99e8QcYP7DkM'
const ENDPOINT = 'https://push.example.invalid/sub-1'

function fakeBrowserPush(page, permission) {
  return page.addInitScript(({ permission, endpoint }) => {
    const SUB = '__fakePushSub'
    const PERM = '__fakePushPerm'
    const store = {
      get: k => localStorage.getItem(k),
      set: (k, v) => localStorage.setItem(k, v),
      del: k => localStorage.removeItem(k),
    }
    const makeSub = ep => ({
      endpoint: ep,
      toJSON: () => ({ endpoint: ep, keys: { p256dh: 'test-p256dh', auth: 'test-auth' } }),
      unsubscribe: async () => { store.del(SUB); return true },
    })
    const pushManager = {
      getSubscription: async () => (store.get(SUB) ? makeSub(store.get(SUB)) : null),
      subscribe: async opts => {
        window.__subscribeKeyBytes = opts.applicationServerKey.length
        window.__userVisibleOnly = opts.userVisibleOnly
        store.set(SUB, endpoint)
        return makeSub(endpoint)
      },
    }
    // No real service worker: requests stay on the page, where page.route sees them.
    Object.defineProperty(navigator.serviceWorker, 'register', {
      value: async () => ({ scope: '/', pushManager }),
    })
    Object.defineProperty(Notification, 'permission', { get: () => store.get(PERM) || 'default' })
    Notification.requestPermission = async () => {
      window.__permissionRequested = true
      store.set(PERM, permission)
      return permission
    }
    // Headless pages always report focus; let the test flip it.
    document.hasFocus = () => window.__focused !== false
  }, { permission, endpoint: ENDPOINT })
}

async function routePushAPI(page) {
  const calls = { subscribe: [], unsubscribe: [], presence: [] }
  await page.route('**/api/push/config', route => route.fulfill({
    json: { enabled: true, vapidPublicKey: VAPID_KEY, subject: 'mailto:test@example.invalid' },
  }))
  for (const kind of Object.keys(calls)) {
    await page.route(`**/api/push/${kind}`, route => {
      calls[kind].push(route.request().postDataJSON())
      return route.fulfill({ json: { ok: true } })
    })
  }
  return calls
}

async function openTweaks(page) {
  await page.locator('button[aria-label="Tweaks"]').click({ timeout: 5000 })
  await expect(page.locator('[data-testid="tweaks-panel"]')).toBeVisible()
}

test.beforeEach(async ({ request }) => {
  await request.post('/__fixture/reset')
})

test('push disabled on the server: no notifications toggle, no permission prompt', async ({ page }) => {
  await fakeBrowserPush(page, 'granted')
  await page.goto('/')
  await openTweaks(page)
  await expect(page.locator('[data-testid="tweaks-panel"]')).toContainText('RIGHT RAIL')
  await expect(page.locator('[data-testid="tweaks-push-switch"]')).toHaveCount(0)
  expect(await page.evaluate(() => window.__permissionRequested || false)).toBe(false)
})

test('enable subscribes with the VAPID key, posts it, reports presence, survives reload, and disables', async ({ page }) => {
  await fakeBrowserPush(page, 'granted')
  const calls = await routePushAPI(page)
  await page.goto('/')
  await openTweaks(page)

  const toggle = page.locator('[data-testid="tweaks-push-switch"]')
  const status = page.locator('[data-testid="tweaks-push-status"]')
  await expect(status).toHaveText('off')
  await toggle.click()

  await expect(status).toHaveText('on when this tab is unfocused')
  await expect(toggle).toHaveAttribute('aria-checked', 'true')
  expect(await page.evaluate(() => window.__permissionRequested)).toBe(true)
  expect(await page.evaluate(() => window.__subscribeKeyBytes)).toBe(65)
  expect(await page.evaluate(() => window.__userVisibleOnly)).toBe(true)
  expect(calls.subscribe).toEqual([{ endpoint: ENDPOINT, keys: { p256dh: 'test-p256dh', auth: 'test-auth' } }])
  await expect.poll(() => calls.presence).toEqual([{ endpoint: ENDPOINT, focused: true }])

  // Looking away flips presence so the server starts notifying.
  await page.evaluate(() => { window.__focused = false; window.dispatchEvent(new Event('blur')) })
  await expect.poll(() => calls.presence.at(-1)).toEqual({ endpoint: ENDPOINT, focused: false })

  // The browser keeps the subscription; a reload re-sends it (a restarted
  // server relearns it) and the toggle comes back on without a new prompt.
  await page.evaluate(() => { window.__focused = true })
  await page.reload()
  await openTweaks(page)
  await expect(status).toHaveText('on when this tab is unfocused')
  await expect.poll(() => calls.subscribe.length).toBe(2)
  expect(calls.subscribe[1].endpoint).toBe(ENDPOINT)

  await toggle.click()
  await expect(status).toHaveText('off')
  expect(calls.unsubscribe).toEqual([{ endpoint: ENDPOINT }])
  expect(await page.evaluate(() => localStorage.getItem('__fakePushSub'))).toBeNull()
})

test('denied permission leaves push off and says why', async ({ page }) => {
  await fakeBrowserPush(page, 'denied')
  const calls = await routePushAPI(page)
  await page.goto('/')
  await openTweaks(page)

  const toggle = page.locator('[data-testid="tweaks-push-switch"]')
  await toggle.click()
  await expect(page.locator('[data-testid="tweaks-push-status"]')).toHaveText('blocked in browser settings')
  await expect(toggle).toHaveAttribute('aria-checked', 'false')
  expect(calls.subscribe).toEqual([])
  expect(calls.presence).toEqual([])
})
