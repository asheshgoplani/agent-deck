import { test, expect } from '@playwright/test'

// #2370: a program in the pane copies with OSC 52 (tmux copy mode, Claude
// Code's selection). The bytes arrive over the session WebSocket and the real
// xterm parser must put the text on the browser clipboard, while a "?" query
// is never answered back into the pane.
test('OSC 52 from the pane writes the browser clipboard and never answers a query', async ({ page, context, request }, testInfo) => {
  test.skip(testInfo.project.name !== 'chromium-desktop', 'clipboard permissions are granted on desktop')
  await context.grantPermissions(['clipboard-read', 'clipboard-write'])
  await request.post('/__fixture/reset')

  // Stand in for the terminal bridge: the page receives exactly the bytes a
  // pane would print, and every byte the terminal sends back is recorded.
  await page.addInitScript(() => {
    const NativeWebSocket = window.WebSocket
    window.__sent = []
    window.WebSocket = class extends EventTarget {
      static OPEN = 1
      constructor(url) {
        super()
        if (!url.includes('/ws/session/')) return new NativeWebSocket(url)
        this.readyState = 1
        window.__pane = this
        queueMicrotask(() => {
          this.dispatchEvent(new Event('open'))
          this.dispatchEvent(new MessageEvent('message', {
            data: JSON.stringify({ type: 'status', event: 'terminal_attached' }),
          }))
          window.__attached = true
        })
      }
      send(data) { window.__sent.push(typeof data === 'string' ? data : '[binary]') }
      close() { this.readyState = 3; this.dispatchEvent(new Event('close')) }
    }
    window.__printToPane = (text) => {
      window.__pane.dispatchEvent(new MessageEvent('message', { data: new TextEncoder().encode(text).buffer }))
    }
  })

  await page.goto('/')
  await page.locator('.sess').first().click()
  await expect(page.locator('.xterm-screen')).toBeVisible()
  await page.waitForFunction(() => window.__attached)
  await page.locator('.xterm-screen').click()
  await page.evaluate(() => navigator.clipboard.writeText('sentinel'))

  // tmux's Ms capability: ESC ] 52 ; c ; <base64> BEL, UTF-8 payload.
  const copied = 'npm run build → ok'
  await page.evaluate((text) => {
    const b64 = btoa(String.fromCharCode(...new TextEncoder().encode(text)))
    window.__printToPane(`\x1b]52;c;${b64}\x07`)
  }, copied)
  await expect.poll(() => page.evaluate(() => navigator.clipboard.readText())).toBe(copied)

  // A read query must leave the clipboard alone and send nothing back.
  const sentBefore = await page.evaluate(() => window.__sent.length)
  await page.evaluate(() => window.__printToPane('\x1b]52;c;?\x07'))
  await page.waitForTimeout(200)
  expect(await page.evaluate(() => navigator.clipboard.readText())).toBe(copied)
  const replies = await page.evaluate((n) => window.__sent.slice(n), sentBefore)
  expect(replies.filter((m) => m.includes(']52;'))).toEqual([])
})
