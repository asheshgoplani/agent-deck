// e2e/mcp-auth.spec.js -- the authenticated browser path for MCP management.
//
// Why this file exists separately from mcps.spec.js:
//
// The shared fixture in helpers/global-setup.js runs with NO bearer token, so
// Server.authorize() short-circuits to "allowed" and every request succeeds
// regardless of headers. That made the whole suite blind to a pane that never
// sends Authorization — which is exactly what shipped: McpPane.js had a private
// fetch helper with no token, so every catalog read and every mutation 401'd
// against a real `agent-deck web --token` deployment while 595 e2e tests stayed
// green.
//
// So this spec boots its OWN fixture WITH --auth-token and drives the real UI:
// click the MCPs tab, click Attach, and require the attachment to appear. The
// negative case loads the same page without the token and requires the pane to
// surface the failure, which proves the positive case actually depends on the
// header rather than passing because auth is off.

import { test, expect } from '@playwright/test'
import { spawn, execFileSync } from 'node:child_process'
import { mkdtempSync, readFileSync, existsSync } from 'node:fs'
import { tmpdir } from 'node:os'
import { join, resolve } from 'node:path'
import { setTimeout as sleep } from 'node:timers/promises'

const REPO_ROOT = resolve(import.meta.dirname, '..', '..', '..')
const BIN_PATH = resolve(REPO_ROOT, 'tests/web/.tmp/web-fixture')
const TOKEN = 'e2e-bearer-token-for-mcp-auth'
const SESSION_ID = 'sess-001'

let proc
let baseURL

test.beforeAll(async () => {
  // global-setup already builds this binary; build defensively so the spec can
  // also run on its own.
  if (!existsSync(BIN_PATH)) {
    execFileSync('go', ['build', '-o', BIN_PATH, './tests/web/fixtures/cmd/web-fixture/'], {
      cwd: REPO_ROOT,
      stdio: 'inherit',
      env: { ...process.env, GOTOOLCHAIN: 'go1.25.13' },
    })
  }

  const portFile = join(mkdtempSync(join(tmpdir(), 'ad-mcp-auth-')), 'port')
  proc = spawn(BIN_PATH, ['--listen', '127.0.0.1:0', '--port-file', portFile, '--auth-token', TOKEN], {
    cwd: REPO_ROOT,
    stdio: ['ignore', 'inherit', 'inherit'],
  })

  const deadline = Date.now() + 15_000
  let port
  while (Date.now() < deadline) {
    if (existsSync(portFile)) {
      port = readFileSync(portFile, 'utf8').trim()
      if (port && port !== '0') break
    }
    await sleep(100)
  }
  if (!port) throw new Error('authenticated web-fixture never published a port')
  baseURL = `http://127.0.0.1:${port}`

  while (Date.now() < deadline) {
    try {
      const res = await fetch(`${baseURL}/healthz`)
      if (res.ok) return
    } catch (_) { /* not up yet */ }
    await sleep(100)
  }
  throw new Error('authenticated web-fixture never became healthy')
})

test.afterAll(async () => {
  if (proc && !proc.killed) proc.kill('SIGTERM')
})

test.describe.configure({ mode: 'serial' })

test.describe('MCP management — authenticated browser path', () => {
  // The MCP pane is desktop/tablet only, same skip as mcps.spec.js.
  test.skip(({ viewport }) => (viewport?.width || 1280) < 768, 'phone viewport: MCP management UI is desktop/tablet only')

  test.beforeEach(async () => {
    const res = await fetch(`${baseURL}/__fixture/reset`, { method: 'POST' })
    expect(res.status).toBe(204)
  })

  test('the server really is authenticated (guards against a false pass)', async ({ page }) => {
    const noToken = await page.request.get(`${baseURL}/api/mcps`)
    expect(noToken.status()).toBe(401)

    const withToken = await page.request.get(`${baseURL}/api/mcps`, {
      headers: { Authorization: `Bearer ${TOKEN}` },
    })
    expect(withToken.status()).toBe(200)
  })

  test('attaching an MCP through the UI succeeds against an authenticated server', async ({ page }) => {
    // main.js reads ?token= into authTokenSignal and strips it from the URL.
    await page.goto(`${baseURL}/s/${SESSION_ID}?token=${TOKEN}`)

    await page.getByRole('button', { name: 'MCPs', exact: true }).click()
    await expect(page.getByTestId('mcp-pane')).toBeVisible()

    // A 401 renders here. Requiring it absent is the actual regression
    // assertion: before the fix, the catalog read failed and this filled in
    // with "unauthorized".
    await expect(page.getByTestId('mcp-error')).toBeHidden()

    // The catalog only renders if the authenticated GET /api/mcps succeeded.
    const attachButton = page.getByTestId('mcp-attach-exa')
    await expect(attachButton).toBeVisible()
    await attachButton.click()

    // The mutation (POST) and the refresh (GET) must both carry the token.
    await expect(page.getByTestId('mcp-attached-exa')).toBeVisible()
    await expect(page.getByTestId('mcp-error')).toBeHidden()

    // Confirm it landed server-side, not just in local component state.
    const list = await page.request.get(`${baseURL}/api/sessions/${SESSION_ID}/mcps`, {
      headers: { Authorization: `Bearer ${TOKEN}` },
    })
    expect(list.status()).toBe(200)
    expect((await list.json()).local).toContain('exa')
  })

  test('detaching through the UI also carries the token', async ({ page }) => {
    await fetch(`${baseURL}/api/sessions/${SESSION_ID}/mcps/exa`, {
      method: 'POST',
      headers: { Authorization: `Bearer ${TOKEN}`, 'Content-Type': 'application/json', Origin: baseURL },
      body: JSON.stringify({ scope: 'local' }),
    })

    await page.goto(`${baseURL}/s/${SESSION_ID}?token=${TOKEN}`)
    await page.getByRole('button', { name: 'MCPs', exact: true }).click()
    await expect(page.getByTestId('mcp-attached-exa')).toBeVisible()

    await page.getByTestId('mcp-detach-exa').click()
    await expect(page.getByTestId('mcp-attached-exa')).toBeHidden()
    await expect(page.getByTestId('mcp-error')).toBeHidden()
  })

  test('a session whose tool has no MCP store says so instead of offering buttons', async ({ page }) => {
    // sess-004 is a shell session. The server refuses MCP routes for it, so the
    // pane must not render a catalog whose every button would fail.
    await page.goto(`${baseURL}/s/sess-004?token=${TOKEN}`)
    await page.getByRole('button', { name: 'MCPs', exact: true }).click()

    await expect(page.getByTestId('mcp-unsupported-tool')).toBeVisible()
    await expect(page.getByTestId('mcp-unsupported-tool')).toContainText('shell')
    await expect(page.getByTestId('mcp-attach-exa')).toHaveCount(0)

    // And the API agrees, so the UI is not merely hiding a working surface.
    const res = await page.request.get(`${baseURL}/api/sessions/sess-004/mcps`, {
      headers: { Authorization: `Bearer ${TOKEN}` },
    })
    expect(res.status()).toBe(400)
  })

  test('without the token no MCP catalog is reachable through the UI', async ({ page }) => {
    // The HTML shell is served unauthenticated on purpose (handleIndex does not
    // authorize, so the page can boot and read ?token= out of the URL), but
    // every data path behind it is authenticated: the menu SSE 401s, so no
    // session ever populates and the pane cannot reach a catalog. The point of
    // this case is the anti-false-pass one — if an attach button were reachable
    // here, the passing test above would not be proving anything about the
    // Authorization header.
    await page.goto(`${baseURL}/s/${SESSION_ID}`)
    await page.getByRole('button', { name: 'MCPs', exact: true }).click()

    // Deliberately no assertion that the pane mounts: with the menu SSE
    // rejected there is no session to select, so the MCP pane is never reached
    // at all. What matters is that nothing actionable appears.
    await expect(page.getByTestId('mcp-attach-exa')).toHaveCount(0)
    await expect(page.getByTestId('mcp-attached-exa')).toHaveCount(0)
    await expect(page.getByTestId('mcp-catalog')).toHaveCount(0)
  })
})
