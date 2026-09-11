// e2e/service-worker-recovery.spec.js -- network-loss recovery through the
// production service worker. EventSource only retries network failures; a
// synthetic HTTP error permanently closes the stream.

import { test, expect } from '@playwright/test'

const RECOVERED_TITLE = 'recovered-after-offline'
const OLD_SERVICE_WORKER = `
self.addEventListener('install', () => self.skipWaiting())
self.addEventListener('activate', event => event.waitUntil(self.clients.claim()))
`

test.describe('service worker SSE recovery', () => {
  test.beforeEach(async ({ request }) => {
    const reset = await request.post('/__fixture/reset')
    expect(reset.status()).toBe(204)
  })

  test('existing menu and command-center streams recover after an offline fetch', async ({ context, page, request }) => {
    let serveOldWorker = true
    await context.route('**/sw.js', async (route) => {
      if (serveOldWorker) {
        await route.fulfill({
          status: 200,
          contentType: 'application/javascript; charset=utf-8',
          headers: { 'Cache-Control': 'no-store', 'Service-Worker-Allowed': '/' },
          body: OLD_SERVICE_WORKER,
        })
        return
      }
      await route.continue()
    })

    // Retain references to the EventSource objects created by main.js. This
    // observes the app's singleton lifecycle without replacing native SSE.
    await page.addInitScript(() => {
      const NativeEventSource = window.EventSource
      window.__agentDeckAppSSE = []
      window.EventSource = new Proxy(NativeEventSource, {
        construct(Target, args) {
          const source = new Target(...args)
          const state = { source, errors: 0 }
          source.addEventListener('error', () => { state.errors += 1 })
          window.__agentDeckAppSSE.push(state)
          return source
        },
      })
    })

    await page.goto('/')
    await expect(page.locator('.sess')).toHaveCount(4, { timeout: 5000 })

    // First install a distinct older worker and let it control this page.
    await page.evaluate(async () => { await navigator.serviceWorker.ready })
    await page.reload()
    await page.waitForFunction(() => navigator.serviceWorker.controller !== null)
    await page.waitForFunction(() => window.__agentDeckAppSSE.length === 2)

    // Serve the production worker and prove skipWaiting + clients.claim
    // activate the changed script in this already-open document.
    serveOldWorker = false
    const activatedInPlace = await page.evaluate(async () => {
      const registration = await navigator.serviceWorker.getRegistration('/')
      const previous = navigator.serviceWorker.controller
      await new Promise((resolve, reject) => {
        const timeout = setTimeout(() => reject(new Error('service worker update timed out')), 5000)
        navigator.serviceWorker.addEventListener('controllerchange', () => {
          clearTimeout(timeout)
          resolve()
        }, { once: true })
        registration.update().catch(reject)
      })
      return navigator.serviceWorker.controller !== previous
    })
    expect(activatedInPlace).toBe(true)
    await context.unroute('**/sw.js')

    await context.setOffline(true)

    // REST callers keep the structured 503 fallback while offline.
    const apiFallback = await page.evaluate(async () => {
      const response = await fetch('/api/menu')
      return { status: response.status, body: await response.json() }
    })
    expect(apiFallback.status).toBe(503)
    expect(apiFallback.body.error.code).toBe('SERVER_UNAVAILABLE')

    // Replace the app-owned streams while offline so their first request
    // deterministically crosses the service worker's failed-fetch branch.
    // These become main.js's live singleton references for the recovery.
    await page.evaluate(async () => {
      const app = await import('/static/app/main.js')
      app.stopSSE()
      app.startSSE()
      app.startCommandCenterSSE()
    })
    await page.waitForFunction(() => window.__agentDeckAppSSE.length === 4)

    await expect.poll(
      () => page.evaluate(() => window.__agentDeckAppSSE.slice(-2).every(state => state.errors > 0)),
      { timeout: 5000 },
    ).toBe(true)
    const offlineStates = await page.evaluate(() =>
      window.__agentDeckAppSSE.slice(-2).map(state => state.source.readyState),
    )
    expect(offlineStates).toEqual([0, 0])

    const titleUpdate = await request.patch('/api/sessions/sess-003', {
      data: { title: RECOVERED_TITLE },
    })
    expect(titleUpdate.status()).toBe(200)
    const statusUpdate = await request.post('/__fixture/session/sess-002/status?to=waiting')
    expect(statusUpdate.status()).toBe(204)

    await context.setOffline(false)

    // The existing menu source must repaint the DOM with the new title.
    await expect(page.locator('.sess .tt', { hasText: RECOVERED_TITLE })).toHaveCount(1, { timeout: 10000 })

    // The existing command-center source must also repaint its live totals.
    if ((page.viewportSize()?.width || 1280) < 768) {
      await page.locator('[data-testid="mobile-tab-command-center"]').click()
    } else {
      await page.locator('.top-tab', { hasText: 'Command Center' }).click()
    }
    await expect(page.locator('[data-testid="cc-totals"]')).toContainText('0 running', { timeout: 10000 })
    await expect(page.locator('[data-testid="cc-totals"]')).toContainText('1 waiting')
  })
})
