// e2e/terminal-links.spec.js -- link clicks in mouse-capturing sessions (#2675)
// and file:// links (#2676).
//
// With tmux `mouse on` or fullscreen Claude Code, the app turns on mouse
// reporting and xterm forwards every click to it, so a plain click on an OSC 8
// link does not open. Cmd-click (macOS) now opens the hovered
// link through the same confirm / trusted-domain policy and the click is not
// forwarded to the app. A file:// link can't load from the http origin, so it
// copies the path and shows a toast instead of opening a blank tab.
//
// The pane is faked the same way terminal-selection.spec.js does it: the
// WebSocket emits the link text plus the mouse-reporting escape codes a
// mouse-capturing tmux pane sends (?1000h + ?1006h), and records what the
// terminal sends back, so "did the app get the click?" is observable.
//
// The plain click is only checked for its mouse report. In this fake pane the
// vendored xterm can still open the link on a plain click too; in real
// sessions it does not (#2675). The modifier path must not depend on either.

import { test, expect } from '@playwright/test'

const WEB_LINK = 'https://example.com/docs'
const FILE_LINK = 'file:///etc/hosts'

async function bootCapturingPane(page) {
  await page.addInitScript(({ webLink, fileLink }) => {
    Object.defineProperty(navigator, 'platform', { get: () => 'MacIntel' })
    window.__confirmCalls = []
    window.__openedLinks = []
    window.__openCalls = 0
    window.__sentInput = []
    window.confirm = (message) => {
      window.__confirmCalls.push(message)
      return true
    }
    window.open = () => {
      window.__openCalls++
      return {
        opener: {},
        location: {
          set href(value) { window.__openedLinks.push(value) },
          get href() { return window.__openedLinks[window.__openedLinks.length - 1] },
        },
      }
    }
    const osc8 = (url, text) => `\x1b]8;;${url}\x1b\\${text}\x1b]8;;\x1b\\`
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
          const out = '\x1b[2J\x1b[H' + osc8(webLink, 'open the docs here') + '\r\n' +
            osc8(fileLink, 'the hosts file here') + '\r\n\x1b[?1000h\x1b[?1006h'
          this.dispatchEvent(new MessageEvent('message', { data: new TextEncoder().encode(out).buffer }))
          window.__terminalOutputSent = true
        })
      }
      send(data) {
        try {
          const msg = JSON.parse(data)
          if (msg.type !== 'input') return
          window.__sentInput.push(msg.data)
          // Like tmux or Claude Code, the app redraws when it gets a click.
          if (msg.data.includes('\x1b[<')) {
            const redraw = '\x1b[s\x1b[H' + osc8(webLink, 'open the docs here') + '\r\n' +
              osc8(fileLink, 'the hosts file here') + '\x1b[u'
            setTimeout(() => this.dispatchEvent(new MessageEvent('message', {
              data: new TextEncoder().encode(redraw).buffer,
            })), 0)
          }
        } catch (_) { /* binary frames are not input */ }
      }
      close() { this.readyState = 3; this.dispatchEvent(new Event('close')) }
    }
  }, { webLink: WEB_LINK, fileLink: FILE_LINK })

  await page.goto('/')
  await page.locator('.sess').first().click()
  const screen = page.locator('.xterm-screen')
  await expect(screen).toBeVisible()
  await page.waitForFunction(() => window.__terminalOutputSent)
  await page.waitForTimeout(100)
  return screen.boundingBox()
}

// SGR mouse reports look like ESC [ < b ; x ; y M -- what tmux would receive.
const mouseReports = (page) =>
  page.evaluate(() => window.__sentInput.filter((d) => d.includes('\x1b[<')).length)

test.beforeEach(async ({ request }, testInfo) => {
  test.skip(testInfo.project.name !== 'chromium-desktop', 'pointer clicks are covered on desktop')
  await request.post('/__fixture/reset')
})

test('Cmd-click opens an OSC 8 link in a mouse-capturing pane without forwarding the click', async ({ page }) => {
  const box = await bootCapturingPane(page)
  const x = box.x + 30
  const y = box.y + 9

  // A plain click still belongs to the app: it is reported as mouse input.
  await page.mouse.move(x, y)
  await page.mouse.click(x, y)
  await expect.poll(() => mouseReports(page)).toBeGreaterThan(0)

  const reportsBefore = await mouseReports(page)
  const opensBefore = await page.evaluate(() => window.__openedLinks.length)
  const confirmsBefore = await page.evaluate(() => window.__confirmCalls.length)
  await page.mouse.move(x + 2, y)
  await page.keyboard.down('Meta')
  await page.mouse.down()
  await page.mouse.up()
  await page.keyboard.up('Meta')

  // Opened exactly once (xterm's own Linkifier never saw the click), through
  // the policy (example.com is not trusted, so it confirmed).
  await expect.poll(() => page.evaluate((n) => window.__openedLinks.slice(n), opensBefore)).toEqual([WEB_LINK])
  expect(await page.evaluate(() => window.__confirmCalls.length)).toBe(confirmsBefore + 1)
  // The modifier-click never reached tmux.
  expect(await mouseReports(page)).toBe(reportsBefore)
})

test('Cmd-click on a file:// link copies the path instead of opening a blank tab', async ({ page, context }) => {
  await context.grantPermissions(['clipboard-read', 'clipboard-write'])
  const box = await bootCapturingPane(page)
  const x = box.x + 30
  const y = box.y + 26

  await page.evaluate(() => navigator.clipboard.writeText('sentinel'))
  await page.mouse.move(x, y)
  await page.mouse.move(x + 2, y)
  await page.keyboard.down('Meta')
  await page.mouse.down()
  await page.mouse.up()
  await page.keyboard.up('Meta')

  await expect(page.getByTestId('toast').filter({ hasText: '/etc/hosts' })).toBeVisible()
  await expect.poll(() => page.evaluate(() => navigator.clipboard.readText())).toBe('/etc/hosts')
  expect(await page.evaluate(() => window.__openCalls)).toBe(0)
  expect(await page.evaluate(() => window.__confirmCalls.length)).toBe(0)
})
