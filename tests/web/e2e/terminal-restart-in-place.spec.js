import { test, expect } from '@playwright/test'

// #2432: a stopped session's terminal must come back without leaving it.
//
// The fixture has no tmux, so a stand-in socket plays the terminal bridge:
// while window.__tmuxGone is true every /ws/session/ connection gets the
// server's TMUX_SESSION_NOT_FOUND error (the #782 banner), and once it is
// false a connection attaches. Everything else, the restart/start endpoints
// and the SSE status feed included, is the real web server.

const SID = 'sess-001'

async function installBridge(page) {
  await page.addInitScript(() => {
    const NativeWebSocket = window.WebSocket
    window.__tmuxGone = true
    window.__wsCount = 0
    window.__attachedCount = 0
    window.WebSocket = class extends EventTarget {
      static OPEN = 1
      constructor(url) {
        super()
        if (!url.includes('/ws/session/')) return new NativeWebSocket(url)
        window.__wsCount += 1
        this.readyState = 1
        const gone = window.__tmuxGone
        queueMicrotask(() => {
          this.dispatchEvent(new Event('open'))
          const payload = gone
            ? { type: 'error', code: 'TMUX_SESSION_NOT_FOUND', message: 'tmux session is not available', hint: 'Restart it.' }
            : { type: 'status', event: 'terminal_attached' }
          this.dispatchEvent(new MessageEvent('message', { data: JSON.stringify(payload) }))
          if (!gone) window.__attachedCount += 1
        })
      }
      send() {}
      close() { this.readyState = 3 }
    }
  })
}

async function openStoppedSession(page, request) {
  await request.post('/__fixture/reset')
  await request.post(`/__fixture/session/${SID}/status?to=stopped`)
  await installBridge(page)
  await page.goto(`/s/${SID}`)
  await expect(page.locator('.xterm-screen')).toBeVisible()
  const banner = page.getByRole('alert').filter({ hasText: 'Terminal disconnected' })
  await expect(banner).toBeVisible()
  return banner
}

test.describe('stopped session terminal restarts in place (#2432)', () => {
  test.beforeEach(({}, testInfo) => {
    test.skip(testInfo.project.name !== 'chromium-desktop', 'terminal + work head layout is the desktop one')
  })

  test('the banner Restart button sits above the xterm layers and restarts the session', async ({ page, request }) => {
    const banner = await openStoppedSession(page, request)
    const button = banner.getByRole('button', { name: 'Restart session' })
    const box = await button.boundingBox()
    const cx = box.x + box.width / 2
    const cy = box.y + box.height / 2

    // Hit testing, not just paint order: the topmost element at the button's
    // centre must be the button, not an xterm canvas or helper layer.
    const hit = await page.evaluate(([x, y]) => {
      const el = document.elementFromPoint(x, y)
      const btn = [...document.querySelectorAll('[role="alert"] button')].find((b) => b.textContent === 'Restart session')
      return { isButton: !!btn && (el === btn || btn.contains(el)), tag: el && (el.className || el.tagName) }
    }, [cx, cy])
    expect(hit.isButton, `elementFromPoint hit ${hit.tag}`).toBe(true)

    await page.evaluate(() => { window.__tmuxGone = false })
    const restart = page.waitForRequest((r) => r.method() === 'POST' && r.url().endsWith(`/api/sessions/${SID}/restart`))
    await page.mouse.click(cx, cy)
    await restart

    await expect(banner).toBeHidden()
    await expect.poll(() => page.evaluate(() => window.__attachedCount)).toBe(1)
  })

  test('Start from the session header reattaches the open terminal', async ({ page, request }) => {
    const banner = await openStoppedSession(page, request)
    expect(await page.evaluate(() => window.__wsCount)).toBe(1)

    await page.evaluate(() => { window.__tmuxGone = false })
    const start = page.waitForResponse((r) => r.request().method() === 'POST' && r.url().endsWith(`/api/sessions/${SID}/start`))
    await page.locator('.work-head').getByRole('button', { name: 'Start', exact: true }).click()
    expect((await start).status()).toBe(200)

    await expect(banner).toBeHidden()
    await expect.poll(() => page.evaluate(() => window.__attachedCount)).toBe(1)
    await expect(page).toHaveURL(new RegExp(`/s/${SID}$`))
  })

  test('a start from outside the web UI (status turns live over SSE) reattaches the open terminal', async ({ page, request }) => {
    const banner = await openStoppedSession(page, request)

    // The CLI or TUI starts the session: the browser only sees the status
    // feed move, no request of its own.
    await page.evaluate(() => { window.__tmuxGone = false })
    await request.post(`/__fixture/session/${SID}/status?to=waiting`)

    await expect(banner).toBeHidden()
    await expect.poll(() => page.evaluate(() => window.__attachedCount)).toBe(1)
    expect(await page.evaluate(() => window.__wsCount)).toBe(2)
  })
})
