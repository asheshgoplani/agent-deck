// e2e/auth-required.spec.js -- a token-protected server opened without (or
// with a wrong) token says so and lets the user sign in.
//
// Before: every API call and the menu stream got 401 and the page looked
// like an empty deck ("No sessions yet. Press n to create one", 0
// everywhere) with no hint that a token was missing.
//
// Boots its OWN fixture with --auth-token (the shared one runs without a
// token), same pattern as mcp-auth.spec.js. Desktop and phone (390x844).
import { test, expect } from '@playwright/test'
import { spawn, execFileSync } from 'node:child_process'
import { mkdtempSync, readFileSync, existsSync } from 'node:fs'
import { tmpdir } from 'node:os'
import { join, resolve } from 'node:path'
import { setTimeout as sleep } from 'node:timers/promises'

const REPO_ROOT = resolve(import.meta.dirname, '..', '..', '..')
const BIN_PATH = resolve(REPO_ROOT, 'tests/web/.tmp/web-fixture')
const TOKEN = 'e2e-bearer-token-for-auth-gate'

let proc
let baseURL

test.use({ serviceWorkers: 'block' })
test.describe.configure({ mode: 'serial' })

test.beforeAll(async () => {
  if (!existsSync(BIN_PATH)) {
    execFileSync('go', ['build', '-o', BIN_PATH, './tests/web/fixtures/cmd/web-fixture/'], {
      cwd: REPO_ROOT, stdio: 'inherit', env: { ...process.env, GOTOOLCHAIN: 'go1.26.11' },
    })
  }
  const portFile = join(mkdtempSync(join(tmpdir(), 'ad-auth-gate-')), 'port')
  proc = spawn(BIN_PATH, ['--listen', '127.0.0.1:0', '--port-file', portFile, '--auth-token', TOKEN], {
    cwd: REPO_ROOT, stdio: ['ignore', 'inherit', 'inherit'],
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
    try { if ((await fetch(`${baseURL}/healthz`)).ok) return } catch (_) { /* not up yet */ }
    await sleep(100)
  }
  throw new Error('authenticated web-fixture never became healthy')
})

test.afterAll(() => { if (proc && !proc.killed) proc.kill('SIGTERM') })

test.beforeEach(async ({ page }, testInfo) => {
  if (testInfo.project.name === 'chromium-phone') await page.setViewportSize({ width: 390, height: 844 })
})

test('without a token the page asks for one instead of showing an empty deck', async ({ page }) => {
  await page.goto(`${baseURL}/`)
  const gate = page.getByTestId('auth-required')
  await expect(gate).toBeVisible({ timeout: 5000 })
  await expect(gate).toContainText('needs an access token')
  await expect(page.getByText('No sessions yet')).toBeHidden()
})

test('a wrong token is reported as rejected', async ({ page }) => {
  await page.goto(`${baseURL}/?token=not-the-token`)
  const gate = page.getByTestId('auth-required')
  await expect(gate).toBeVisible({ timeout: 5000 })
  await expect(gate).toContainText('rejected')
})

test('entering the right token signs in and shows the sessions', async ({ page }) => {
  await page.goto(`${baseURL}/`)
  const gate = page.getByTestId('auth-required')
  await expect(gate).toBeVisible({ timeout: 5000 })
  await gate.getByLabel('Access token').fill(TOKEN)
  await gate.getByRole('button', { name: 'Sign in' }).click()
  await expect(gate).toBeHidden({ timeout: 5000 })
  await page.waitForSelector('.sess', { state: 'attached', timeout: 5000 })
  // The token never stays in the address bar.
  expect(page.url()).not.toContain(TOKEN)
})

test('with the token in the link no sign-in prompt appears', async ({ page }) => {
  await page.goto(`${baseURL}/?token=${TOKEN}`)
  await page.waitForSelector('.sess', { state: 'attached', timeout: 5000 })
  await expect(page.getByTestId('auth-required')).toHaveCount(0)
})
