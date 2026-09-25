// e2e/service-worker-recovery.spec.js -- network-loss recovery through the
// production service worker. EventSource only retries network failures; a
// synthetic HTTP error permanently closes the stream.

import { test, expect } from '@playwright/test'
import { expandSeededCollapsedGroups } from '../helpers/seededSidebar.js'

const RECOVERED_TITLE = 'recovered-after-offline'
const OLD_SERVICE_WORKER = `
self.addEventListener('install', () => self.skipWaiting())
self.addEventListener('activate', event => event.waitUntil(self.clients.claim()))
`

test.describe('service worker SSE recovery', () => {
  test.beforeEach(async ({ request }) => {
    const reset = await request.post('/__fixture/reset')
    expect(reset.status()).toBe(204)
    const clearOutage = await request.post('/__fixture/stream-outage?enabled=false')
    expect(clearOutage.status()).toBe(200)
  })

  test('existing menu and command-center streams recover after a transient outage', async ({ context, page, request }) => {
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
      const nativeFetch = window.fetch.bind(window)
      window.__agentDeckAppSSE = []
      window.__agentDeckSSEProbeStatuses = []
      window.fetch = async (...args) => {
        const response = await nativeFetch(...args)
        const rawURL = typeof args[0] === 'string' ? args[0] : args[0]?.url
        if (rawURL && new URL(rawURL, location.href).pathname.startsWith('/events/')) {
          window.__agentDeckSSEProbeStatuses.push(response.status)
        }
        return response
      }
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

    await expandSeededCollapsedGroups(page)
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

    await context.setOffline(false)

    // Terminate both established response bodies, then return a transient
    // 503 to their native reconnect and to the app's readiness probe.
    const outage = await request.post('/__fixture/stream-outage?enabled=true')
    expect(outage.status()).toBe(200)
    expect((await outage.json()).disconnected).toBe(2)
    await expect.poll(
      () => page.evaluate(() => window.__agentDeckAppSSE.slice(0, 2).map(state => state.source.readyState)),
      { timeout: 5000 },
    ).toEqual([2, 2])

    // The recovery probe must tolerate a transient 503 while the service
    // remains unavailable rather than consuming its only retry.
    await expect.poll(
      () => page.evaluate(() => window.__agentDeckSSEProbeStatuses.filter(status => status === 503).length),
      { timeout: 10000 },
    ).toBeGreaterThanOrEqual(1)

    const restored = await request.post('/__fixture/stream-outage?enabled=false')
    expect(restored.status()).toBe(200)

    const titleUpdate = await request.patch('/api/sessions/sess-003', {
      data: { title: RECOVERED_TITLE },
    })
    expect(titleUpdate.status()).toBe(200)
    const statusUpdate = await request.post('/__fixture/session/sess-002/status?to=waiting')
    expect(statusUpdate.status()).toBe(204)

    await page.waitForFunction(() => window.__agentDeckAppSSE.length === 4)
    await expect.poll(
      () => page.evaluate(() => window.__agentDeckAppSSE.slice(-2).map(state => state.source.readyState)),
      { timeout: 10000 },
    ).toEqual([1, 1])

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

    // Explicit shutdown during a pending transient retry cancels recovery and
    // remains idempotent, even when browser wake events follow.
    const probeCountBeforeStopOutage = await page.evaluate(() =>
      window.__agentDeckSSEProbeStatuses.filter(status => status === 503).length,
    )
    const stopOutage = await request.post('/__fixture/stream-outage?enabled=true')
    expect(stopOutage.status()).toBe(200)
    expect((await stopOutage.json()).disconnected).toBe(2)
    await expect.poll(
      () => page.evaluate(() => window.__agentDeckSSEProbeStatuses.filter(status => status === 503).length),
      { timeout: 10000 },
    ).toBe(probeCountBeforeStopOutage + 2)
    await page.waitForTimeout(100)

    const stoppedCounts = await page.evaluate(async () => {
      const app = await import('/static/app/main.js')
      app.stopSSE()
      app.stopSSE()
      return {
        sources: window.__agentDeckAppSSE.length,
        probes: window.__agentDeckSSEProbeStatuses.length,
      }
    })
    const stopRestored = await request.post('/__fixture/stream-outage?enabled=false')
    expect(stopRestored.status()).toBe(200)
    await page.evaluate(() => {
      window.dispatchEvent(new Event('online'))
      document.dispatchEvent(new Event('visibilitychange'))
    })
    await page.waitForTimeout(2500)
    expect(await page.evaluate(() => window.__agentDeckAppSSE.length)).toBe(stoppedCounts.sources)
    expect(await page.evaluate(() => window.__agentDeckSSEProbeStatuses.length)).toBe(stoppedCounts.probes)
    expect(await page.evaluate(() => window.__agentDeckAppSSE.slice(-2).map(state => state.source.readyState))).toEqual([2, 2])
  })

  test('new native streams remain retryable when their first fetch is offline', async ({ context, page, request }) => {
    await page.goto('/')
    await page.evaluate(async () => { await navigator.serviceWorker.ready })
    await page.reload()
    await page.waitForFunction(() => navigator.serviceWorker.controller !== null)

    await context.setOffline(true)
    await page.evaluate(() => {
      window.__offlineNativeSSE = []
      for (const [path, eventType] of [
        ['/events/menu', 'menu'],
        ['/events/command-center', 'command-center'],
      ]) {
        const source = new EventSource(path)
        const state = { source, errors: 0, snapshots: [] }
        source.addEventListener('error', () => { state.errors += 1 })
        source.addEventListener(eventType, event => { state.snapshots.push(JSON.parse(event.data)) })
        window.__offlineNativeSSE.push(state)
      }
    })
    await page.waitForFunction(() => window.__offlineNativeSSE.every(state => state.errors > 0))
    expect(await page.evaluate(() => window.__offlineNativeSSE.map(state => state.source.readyState))).toEqual([0, 0])

    const update = await request.patch('/api/sessions/sess-003', {
      data: { title: RECOVERED_TITLE },
    })
    expect(update.status()).toBe(200)
    await context.setOffline(false)

    await page.waitForFunction(title => {
      const [menu, commandCenter] = window.__offlineNativeSSE
      return menu.snapshots.some(snapshot => snapshot.items?.some(item => item.session?.title === title)) &&
        commandCenter.snapshots.some(snapshot =>
          snapshot.conductors?.some(conductor =>
            conductor.sessions?.some(session => session.title === title),
          ),
        )
    }, RECOVERED_TITLE)
    await page.evaluate(() => window.__offlineNativeSSE.forEach(state => state.source.close()))
  })

  test('permanent auth failures wait for a browser wake before retrying', async ({ page, request }) => {
    await page.addInitScript(() => {
      const NativeEventSource = window.EventSource
      const nativeFetch = window.fetch.bind(window)
      window.__agentDeckAppSSE = []
      window.__agentDeckSSEProbeStatuses = []
      window.fetch = async (...args) => {
        const response = await nativeFetch(...args)
        const rawURL = typeof args[0] === 'string' ? args[0] : args[0]?.url
        if (rawURL && new URL(rawURL, location.href).pathname.startsWith('/events/')) {
          window.__agentDeckSSEProbeStatuses.push(response.status)
        }
        return response
      }
      window.EventSource = new Proxy(NativeEventSource, {
        construct(Target, args) {
          const source = new Target(...args)
          window.__agentDeckAppSSE.push(source)
          return source
        },
      })
    })

    await page.goto('/')
    await page.waitForFunction(() =>
      window.__agentDeckAppSSE.length === 2 &&
      window.__agentDeckAppSSE.every(source => source.readyState === EventSource.OPEN),
    )

    const outage = await request.post('/__fixture/stream-outage?enabled=true&status=401')
    expect(outage.status()).toBe(200)
    expect((await outage.json()).disconnected).toBe(2)
    await expect.poll(
      () => page.evaluate(() => window.__agentDeckSSEProbeStatuses.filter(status => status === 401).length),
      { timeout: 10000 },
    ).toBe(2)

    await page.waitForTimeout(2500)
    expect(await page.evaluate(() => window.__agentDeckSSEProbeStatuses.filter(status => status === 401).length)).toBe(2)
    expect(await page.evaluate(() => window.__agentDeckAppSSE.length)).toBe(2)

    const restored = await request.post('/__fixture/stream-outage?enabled=false')
    expect(restored.status()).toBe(200)
    await page.evaluate(() => document.dispatchEvent(new Event('visibilitychange')))
    await page.waitForFunction(() =>
      window.__agentDeckAppSSE.length === 4 &&
      window.__agentDeckAppSSE.slice(-2).every(source => source.readyState === EventSource.OPEN),
    )
  })
})
