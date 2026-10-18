// e2e/file-links.spec.js -- file:// links served through the deck (#2676).
//
// A browser on the deck's http(s) origin can't navigate to file://. With
// `[web] file_link_roots` set, a file link under a root opens through
// GET /api/files instead; everything else keeps the copy-path fallback.
//
// What this spec pins, end to end through the real server and a real popup:
//   1. GET /api/settings serves the roots and the drawer shows them.
//   2. Cmd-click on a file link under a root (in a mouse-capturing pane)
//      opens a tab on /api/files that shows the file.
//   3. An HTML file in a root is shown as source text: its script never runs.
//   4. Escape attempts (.., a symlink out, a directory) are refused.
//   5. A file link outside the roots still copies the path.
//
// The fixture is booted per describe with `-file-link-roots`, like
// trusted-domains.spec.js. Desktop project only: the policy is
// viewport-independent.

import { test, expect } from '@playwright/test'
import { spawn } from 'node:child_process'
import { existsSync, mkdirSync, mkdtempSync, readFileSync, realpathSync, rmSync, symlinkSync, writeFileSync } from 'node:fs'
import { tmpdir } from 'node:os'
import { join, resolve } from 'node:path'
import { randomBytes } from 'node:crypto'
import { setTimeout as sleep } from 'node:timers/promises'

const BIN_PATH = resolve(import.meta.dirname, '..', '.tmp', 'web-fixture')

// spawnFixture boots a web-fixture with `extraArgs` on an OS-allocated port
// and returns { base, kill }. Mirrors read-only-mode.spec.js.
async function spawnFixture(extraArgs) {
  if (!existsSync(BIN_PATH)) {
    throw new Error(
      `file-links: fixture binary missing at ${BIN_PATH}. ` +
        'Run via `npm run test:e2e` so global-setup builds it first.',
    )
  }
  const portFile = join(tmpdir(), `adweb-fl-${randomBytes(6).toString('hex')}.port`)
  const child = spawn(
    BIN_PATH,
    ['-listen', '127.0.0.1:0', '-port-file', portFile, ...extraArgs],
    { stdio: ['ignore', 'ignore', 'inherit'] },
  )
  const kill = () => { child.kill('SIGTERM') }

  try {
    const deadline = Date.now() + 10_000
    let port = null
    while (Date.now() < deadline) {
      if (child.exitCode !== null) {
        throw new Error(`file-links web-fixture exited early (code ${child.exitCode})`)
      }
      if (existsSync(portFile)) {
        const txt = readFileSync(portFile, 'utf8').trim()
        if (txt) {
          port = Number(txt)
          break
        }
      }
      await sleep(100)
    }
    if (!port) throw new Error('file-links web-fixture never wrote its port file')
    const base = `http://127.0.0.1:${port}`

    let healthy = false
    while (Date.now() < deadline) {
      try {
        const res = await fetch(`${base}/healthz`)
        if (res.ok) {
          healthy = true
          break
        }
      } catch (_) { /* not up yet */ }
      await sleep(100)
    }
    if (!healthy) throw new Error(`file-links web-fixture never became healthy at ${base}`)
    return { base, kill }
  } catch (err) {
    kill()
    throw err
  } finally {
    rmSync(portFile, { force: true })
  }
}

// Lays out <tmp>/root/{notes.txt,report.html,sub/} plus <tmp>/secret.txt and
// <tmp>/root/link-out -> ../secret.txt. realpath so macOS /var -> /private/var
// doesn't make the client-side root hint disagree with the server.
function makeTree() {
  const tmp = realpathSync(mkdtempSync(join(tmpdir(), 'adweb-fl-tree-')))
  const root = join(tmp, 'root')
  mkdirSync(join(root, 'sub'), { recursive: true })
  writeFileSync(join(root, 'notes.txt'), 'hello from the deck\n')
  writeFileSync(join(root, 'report.html'), '<title>x</title><script>window.opener && (window.opener.__pwned = true); document.title = "PWNED"</script>report body')
  writeFileSync(join(tmp, 'secret.txt'), 'TOP SECRET')
  symlinkSync('../secret.txt', join(root, 'link-out'))
  return { tmp, root, cleanup: () => rmSync(tmp, { recursive: true, force: true }) }
}

// A fake pane like terminal-links.spec.js: prints OSC 8 file links and turns
// on mouse reporting, so the click path is the #2675 modifier path.
async function bootPane(page, base, links) {
  await page.addInitScript((urls) => {
    Object.defineProperty(navigator, 'platform', { get: () => 'MacIntel' })
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
          const out = '\x1b[2J\x1b[H' + urls.map((u, i) => osc8(u, `file link number ${i}`)).join('\r\n') +
            '\r\n\x1b[?1000h\x1b[?1006h'
          this.dispatchEvent(new MessageEvent('message', { data: new TextEncoder().encode(out).buffer }))
          window.__terminalOutputSent = true
        })
      }
      send() {}
      close() { this.readyState = 3; this.dispatchEvent(new Event('close')) }
    }
  }, links)
  await page.goto(`${base}/`)
  await page.waitForSelector('.sess', { timeout: 5000 })
  // Wait for /api/settings hydration of the roots.
  await page.waitForFunction(async () => {
    const state = await import('/static/app/state.js')
    return state.fileLinkRootsSignal.value.length > 0
  }, { timeout: 5000 })
  await page.locator('.sess').first().click()
  const screen = page.locator('.xterm-screen')
  await expect(screen).toBeVisible()
  await page.waitForFunction(() => window.__terminalOutputSent)
  await page.waitForTimeout(100)
  return screen.boundingBox()
}

async function cmdClickRow(page, box, row) {
  const x = box.x + 30
  const y = box.y + 9 + row * 17
  await page.mouse.move(x, y)
  await page.mouse.move(x + 2, y)
  await page.keyboard.down('Meta')
  await page.mouse.down()
  await page.mouse.up()
  await page.keyboard.up('Meta')
}

test.describe('file_link_roots set', () => {
  test.skip(
    ({ viewport }) => (viewport?.width || 1280) !== 1280,
    'desktop project only: the file-link policy is viewport-independent',
  )

  let fixture = null
  let tree = null

  test.beforeAll(async ({}, testInfo) => {
    if ((testInfo.project.use?.viewport?.width || 1280) !== 1280) return
    tree = makeTree()
    fixture = await spawnFixture(['-file-link-roots', tree.root])
  })

  test.afterAll(() => {
    if (fixture) fixture.kill()
    if (tree) tree.cleanup()
    fixture = null
  })

  test('GET /api/settings serves the roots and the drawer shows them', async ({ page, request }) => {
    const res = await request.get(`${fixture.base}/api/settings`)
    expect((await res.json()).fileLinkRoots).toEqual([tree.root])

    await page.goto(`${fixture.base}/`)
    await page.waitForSelector('.sess', { timeout: 5000 })
    await page.keyboard.press('Control+k')
    await expect(page.locator('[data-testid="command-palette"]')).toBeVisible()
    await page.locator('[data-testid="palette-cmd-row"]', { hasText: 'Settings drawer' }).click()
    await expect(page.locator('[data-testid="settings-file-link-roots"] .v')).toHaveText(tree.root)
  })

  test('Cmd-click on a file link under a root opens it through the deck', async ({ page, context }) => {
    const box = await bootPane(page, fixture.base, [`file://${tree.root}/notes.txt`])
    const popup = context.waitForEvent('page')
    await cmdClickRow(page, box, 0)
    const tab = await popup
    await tab.waitForLoadState()
    expect(tab.url()).toBe(`${fixture.base}/api/files?path=${encodeURIComponent(`${tree.root}/notes.txt`)}`)
    await expect(tab.locator('body')).toHaveText('hello from the deck')
  })

  test('an HTML file is shown as source and its script never runs', async ({ page, context }) => {
    const box = await bootPane(page, fixture.base, [`file://${tree.root}/report.html`])
    const popup = context.waitForEvent('page')
    await cmdClickRow(page, box, 0)
    const tab = await popup
    await tab.waitForLoadState()
    await expect(tab.locator('body')).toContainText('<script>')
    expect(await tab.title()).not.toBe('PWNED')
    expect(await page.evaluate(() => window.__pwned)).toBeUndefined()
  })

  test('escape attempts are refused', async ({ request }) => {
    for (const path of [`${tree.root}/../secret.txt`, `${tree.root}/link-out`, `${tree.tmp}/secret.txt`, `${tree.root}/sub`]) {
      const res = await request.get(`${fixture.base}/api/files?path=${encodeURIComponent(path)}`)
      expect(res.status(), path).toBe(403)
      expect(await res.text()).not.toContain('TOP SECRET')
    }
  })

  test('a file link outside the roots still copies the path', async ({ page, context }) => {
    await context.grantPermissions(['clipboard-read', 'clipboard-write'])
    const box = await bootPane(page, fixture.base, [`file://${tree.tmp}/secret.txt`])
    let popups = 0
    context.on('page', () => { popups++ })
    await cmdClickRow(page, box, 0)
    await expect(page.getByTestId('toast').filter({ hasText: 'secret.txt' })).toBeVisible()
    await expect.poll(() => page.evaluate(() => navigator.clipboard.readText())).toBe(`${tree.tmp}/secret.txt`)
    expect(popups).toBe(0)
  })
})
