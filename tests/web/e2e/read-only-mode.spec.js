// e2e/read-only-mode.spec.js -- webMutations=false gating, end to end.
//
// The shared fixture (spawned by helpers/global-setup.js) always runs with
// mutations ALLOWED, so this spec boots its OWN web-fixture instance with
// `-allow-mutations=false` on an OS-allocated ephemeral port and talks to it
// with absolute URLs (the suite baseURL points at the shared fixture).
//
// The binary at tests/web/.tmp/web-fixture is built by global-setup before
// any spec runs, so it is guaranteed to exist here. We resolve the bound
// port via the fixture's `-port-file` flag (see fixtures/cmd/web-fixture/
// main.go) and kill the child in afterAll.
//
// Gating facts this spec pins (verified against source):
//   - Server: every mutating handler calls Server.checkMutationsAllowed
//     (internal/web/server.go) which writes HTTP 403 with error code
//     "MUTATIONS_DISABLED" (ErrCodeForbidden, internal/web/api_types.go)
//     before reading the request body.
//   - Client hydration: AppShell.js fetches GET /api/settings and copies
//     `webMutations` into mutationsEnabledSignal (default true in state.js,
//     so create affordances flash once and then disappear — assertions
//     below auto-retry through that).
//   - Hidden when disabled: WorkHead action cluster incl. the "New" button
//     (AppShell.js `canMutate &&`), the sidebar "+" icon-button
//     (Sidebar.js `mutationsEnabledSignal.value &&`), the palette
//     "New session" command (CommandPalette.js unshifts it only when the
//     signal is true), and the `n` shortcut (AppShell.js keydown handler
//     checks the signal).
//   - NOT hidden when disabled: the per-row hover action buttons in
//     Sidebar.js (start/stop/restart/edit/delete) always render; their
//     shared doAction() guard short-circuits with the "mutations disabled"
//     toast instead. We assert that actual behavior rather than absence.
//
// Spawning an extra server per project is wasteful, so this spec runs on
// chromium-desktop only (viewport width === 1280).

import { test, expect } from '@playwright/test'
import { spawn } from 'node:child_process'
import { existsSync, readFileSync, rmSync } from 'node:fs'
import { tmpdir } from 'node:os'
import { join, resolve } from 'node:path'
import { randomBytes } from 'node:crypto'
import { setTimeout as sleep } from 'node:timers/promises'

const BIN_PATH = resolve(import.meta.dirname, '..', '.tmp', 'web-fixture')

let child = null
let base = null

async function spawnReadOnlyFixture() {
  if (!existsSync(BIN_PATH)) {
    throw new Error(
      `read-only-mode: fixture binary missing at ${BIN_PATH}. ` +
        'Run via `npm run test:e2e` so global-setup builds it first.',
    )
  }
  const portFile = join(tmpdir(), `adweb-ro-${randomBytes(6).toString('hex')}.port`)
  child = spawn(
    BIN_PATH,
    ['-listen', '127.0.0.1:0', '-allow-mutations=false', '-port-file', portFile],
    { stdio: ['ignore', 'ignore', 'inherit'] },
  )

  try {
    // Poll the port file (written once the listener is bound), then /healthz.
    const deadline = Date.now() + 10_000
    let port = null
    while (Date.now() < deadline) {
      if (child.exitCode !== null) {
        throw new Error(`read-only web-fixture exited early (code ${child.exitCode})`)
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
    if (!port) throw new Error('read-only web-fixture never wrote its port file')
    base = `http://127.0.0.1:${port}`

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
    if (!healthy) throw new Error(`read-only web-fixture never became healthy at ${base}`)
  } catch (err) {
    // Failure path: don't leak the child; afterAll won't see a usable state.
    killFixture()
    throw err
  } finally {
    rmSync(portFile, { force: true })
  }
}

function killFixture() {
  if (child) {
    child.kill('SIGTERM')
    child = null
  }
}

// The app mounts with mutationsEnabledSignal=true (state.js default) until
// the GET /api/settings hydration flips it. Waiting for the sidebar "+" to
// reach count 0 is the deterministic post-hydration barrier: the button is
// rendered pre-hydration and removed after, and toHaveCount(0) auto-retries.
async function gotoHydratedReadOnlyApp(page) {
  await page.goto(`${base}/`)
  await page.waitForSelector('.sess', { timeout: 5000 })
  await expect(page.locator('.side-head button[aria-label="New session"]')).toHaveCount(0)
}

test.describe('read-only mode (webMutations=false)', () => {
  test.skip(
    ({ viewport }) => (viewport?.width || 1280) !== 1280,
    'desktop project only: gating logic is viewport-independent; one extra server is enough',
  )

  test.beforeAll(async ({}, testInfo) => {
    if ((testInfo.project.use?.viewport?.width || 1280) !== 1280) return
    await spawnReadOnlyFixture()
  })

  test.afterAll(() => {
    killFixture()
  })

  test('GET /api/settings reports webMutations:false', async ({ request }) => {
    const res = await request.get(`${base}/api/settings`)
    expect(res.status()).toBe(200)
    const body = await res.json()
    expect(body.profile).toBe('fixture')
    expect(body.webMutations).toBe(false)
  })

  test('create affordances are hidden: WorkHead "New" button and sidebar "+"', async ({ page }) => {
    await gotoHydratedReadOnlyApp(page)
    // Sidebar "+" already asserted absent by the hydration barrier; re-state
    // it explicitly for the report, then check the WorkHead action cluster.
    await expect(page.locator('.side-head button[aria-label="New session"]')).toHaveCount(0)
    // AppShell WorkHead renders the whole .actions div (Start/Stop/Restart/
    // Fork/New) only when canMutate — gone entirely in read-only mode.
    await expect(page.locator('.work-head .actions')).toHaveCount(0)
  })

  test('sidebar row action buttons stay rendered but are click-gated with a toast', async ({ page, request }) => {
    await gotoHydratedReadOnlyApp(page)
    // Per Sidebar.js the per-row hover buttons are NOT gated at render time;
    // doAction() rejects with addToast('mutations disabled') before any
    // apiFetch. Hover to reveal the action bar (CSS: .sess:hover .actions).
    const row = page.locator('.sess').first()
    await row.hover()
    const delBtn = row.locator('.actions button[title="Delete"]')
    await expect(delBtn).toBeVisible()
    await delBtn.click()
    // Toast surfaces the gate; no confirm dialog opens.
    await expect(page.locator('.toast', { hasText: 'mutations disabled' })).toBeVisible({ timeout: 2000 })
    await expect(page.locator('.overlay .dialog')).toHaveCount(0)
    // And the backend state is untouched: all four seeded sessions remain.
    const res = await request.get(`${base}/api/sessions`)
    expect(res.status()).toBe(200)
    const body = await res.json()
    expect(body.sessions.length).toBe(4)
  })

  test('the n shortcut does NOT open the create dialog', async ({ page }) => {
    await gotoHydratedReadOnlyApp(page)
    await page.keyboard.press('n')
    // Give the (gated) handler a beat, then assert nothing opened.
    await page.waitForTimeout(300)
    await expect(page.locator('.overlay .dialog')).toHaveCount(0)
  })

  test('command palette omits the "New session" command', async ({ page }) => {
    await gotoHydratedReadOnlyApp(page)
    await page.keyboard.press('Control+k')
    await expect(page.locator('[data-testid="command-palette"]')).toBeVisible()
    // Prove the COMMANDS section rendered (gated-independent command present)…
    await expect(
      page.locator('[data-testid="palette-cmd-row"]', { hasText: 'Settings drawer' }),
    ).toBeVisible()
    // …and the mutation-gated entry is absent (CommandPalette.js only
    // unshifts "New session" when mutationsEnabledSignal is true).
    await expect(
      page.locator('[data-testid="palette-cmd-row"]', { hasText: 'New session' }),
    ).toHaveCount(0)
  })

  test('direct POST /api/sessions returns 403 MUTATIONS_DISABLED', async ({ request }) => {
    const res = await request.post(`${base}/api/sessions`, {
      data: { title: 'should-not-exist', tool: 'shell', projectPath: '/tmp/nope' },
    })
    // server.go checkMutationsAllowed → 403 + ErrCodeForbidden, checked
    // before body validation, so a fully valid payload still gets refused.
    expect(res.status()).toBe(403)
    const body = await res.json()
    expect(body.error?.code).toBe('MUTATIONS_DISABLED')
    expect(body.error?.message).toContain('web mutations are disabled')
    // Nothing was created.
    const list = await request.get(`${base}/api/sessions`)
    expect((await list.json()).sessions.length).toBe(4)
  })
})
