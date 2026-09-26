import { test, expect } from '@playwright/test'

test('Option-drag selects past mouse reporting and copies the selected text', async ({ page, context, request }, testInfo) => {
  test.skip(testInfo.project.name !== 'chromium-desktop', 'mouse selection is covered on desktop')
  await context.grantPermissions(['clipboard-read', 'clipboard-write'])
  await request.post('/__fixture/reset')

  // A captured mouse is represented by xterm's mouse-reporting escape code.
  // Feed terminal output through the same WebSocket events as the real bridge.
  await page.addInitScript(() => {
    Object.defineProperty(navigator, 'platform', { get: () => 'MacIntel' })
    const NativeWebSocket = window.WebSocket
    window.WebSocket = class extends EventTarget {
      static OPEN = 1
      constructor(url) {
        super()
        if (!url.includes('/ws/session/')) return new NativeWebSocket(url)
        this.readyState = 1
        queueMicrotask(() => {
          this.dispatchEvent(new Event('open'))
          this.dispatchEvent(new MessageEvent('message', {
            data: JSON.stringify({ type: 'status', event: 'terminal_attached' }),
          }))
          this.dispatchEvent(new MessageEvent('message', {
            data: new TextEncoder().encode('\x1b[2J\x1b[Halpha beta gamma\r\n\x1b[?1000h\x1b[?1006h').buffer,
          }))
          window.__terminalOutputSent = true
        })
      }
      send() {}
      close() { this.readyState = 3; this.dispatchEvent(new Event('close')) }
    }
  })

  await page.goto('/')
  await page.locator('.sess').first().click()
  const screen = page.locator('.xterm-screen')
  await expect(screen).toBeVisible()
  await page.waitForFunction(() => window.__terminalOutputSent)
  await page.waitForTimeout(100)

  const box = await screen.boundingBox()
  const y = box.y + 9
  await page.evaluate(() => navigator.clipboard.writeText('sentinel'))
  await page.mouse.move(box.x + 5, y)
  await page.mouse.down()
  await page.mouse.move(box.x + 85, y, { steps: 5 })
  await page.mouse.up()
  await expect.poll(() => page.evaluate(() => navigator.clipboard.readText())).toBe('sentinel')

  await page.mouse.move(box.x + 5, y)
  await page.keyboard.down('Alt')
  await page.mouse.down()
  await page.mouse.move(box.x + 85, y, { steps: 5 })
  await page.mouse.up()
  await page.keyboard.up('Alt')
  await expect.poll(() => page.evaluate(() => navigator.clipboard.readText())).toContain('beta')

  const selected = await page.evaluate(() => navigator.clipboard.readText())
  await page.mouse.click(box.x + 5, box.y + 40)
  await expect.poll(() => page.evaluate(() => navigator.clipboard.readText())).toBe(selected)
})
