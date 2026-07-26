// e2e/trusted-domains.spec.js -- `[web].trusted_domains` allowlist (#1682).
//
// xterm.js ships a default OSC-8 activate handler that always fires
// `confirm("Do you want to navigate to <url>? WARNING: This link could
// potentially be dangerous")`. Anyone clicking self-hosted GitLab / Gerrit /
// CI links all day pays that prompt several times a minute. TerminalPanel.js
// now installs the `linkHandler` from terminalLinks.js, which consults the
// config served by GET /api/settings:
//
//   [web]
//   trusted_domains   = ["gitlab.corp.example", "*.ci.corp.example"]
//   confirm_link_open = true   # default; false accepts the risk globally
//
// What this spec pins, end to end through the real server:
//   1. GET /api/settings carries `trustedDomains` + `confirmLinkOpen` from
//      web.Config (internal/web/handlers_settings.go).
//   2. AppShell.js hydrates trustedDomainsSignal / confirmLinkOpenSignal from
//      that response, and the settings drawer renders both.
//   3. Activating an ALLOWLISTED link opens it with NO confirm; activating a
//      NON-allowlisted link still confirms — the core of the issue.
//   4. `confirm_link_open = false` drops the prompt for every host.
//
// Why the handler is driven directly instead of clicking rendered link cells:
// an OSC-8 hyperlink only exists in the terminal buffer once a live tmux pane
// emits one, and the in-memory fixture has no pane (its /ws/session endpoint
// has no tmux behind it). So the spec imports the SAME production module
// instance the app loaded (`/static/app/terminalLinks.js` — identical URL, so
// the browser reuses the module record and its hydrated signals) and calls the
// `activate` callback xterm itself would call. The decision path under test is
// the real one, in a real browser, against real hydrated config.
//
// The shared fixture (helpers/global-setup.js) runs with no trusted domains,
// so each describe block boots its own web-fixture on an ephemeral port with
// the flags it needs, exactly like read-only-mode.spec.js. Extra servers are
// wasteful, so this spec runs on chromium-desktop only — the policy is
// viewport-independent.

import { test, expect } from '@playwright/test'
import { spawn } from 'node:child_process'
import { existsSync, readFileSync, rmSync } from 'node:fs'
import { tmpdir } from 'node:os'
import { join, resolve } from 'node:path'
import { randomBytes } from 'node:crypto'
import { setTimeout as sleep } from 'node:timers/promises'

const BIN_PATH = resolve(import.meta.dirname, '..', '.tmp', 'web-fixture')

const TRUSTED_EXACT = 'gitlab.corp.example'
const TRUSTED_WILDCARD = '*.ci.corp.example'

// spawnFixture boots a web-fixture with `extraArgs` on an OS-allocated port
// and returns { base, kill }. Mirrors read-only-mode.spec.js.
async function spawnFixture(extraArgs) {
  if (!existsSync(BIN_PATH)) {
    throw new Error(
      `trusted-domains: fixture binary missing at ${BIN_PATH}. ` +
        'Run via `npm run test:e2e` so global-setup builds it first.',
    )
  }
  const portFile = join(tmpdir(), `adweb-td-${randomBytes(6).toString('hex')}.port`)
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
        throw new Error(`trusted-domains web-fixture exited early (code ${child.exitCode})`)
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
    if (!port) throw new Error('trusted-domains web-fixture never wrote its port file')
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
    if (!healthy) throw new Error(`trusted-domains web-fixture never became healthy at ${base}`)
    return { base, kill }
  } catch (err) {
    kill()
    throw err
  } finally {
    rmSync(portFile, { force: true })
  }
}

// Records confirm() calls and window.open() navigations instead of showing a
// real dialog, so the assertion "was the user prompted?" is observable. The
// stub keeps xterm's contract: open() returns a window whose `opener` is
// cleared before `location.href` is assigned.
async function stubLinkOpening(page) {
  await page.addInitScript(() => {
    window.__confirmCalls = []
    window.__openedLinks = []
    window.confirm = (message) => {
      window.__confirmCalls.push(message)
      return true
    }
    window.open = () => ({
      opener: {},
      location: {
        set href(value) { window.__openedLinks.push(value) },
        get href() { return window.__openedLinks[window.__openedLinks.length - 1] },
      },
    })
  })
}

// Loads the app and blocks until AppShell's /api/settings hydration has
// reached the link-policy signals. state.js starts at the safe defaults
// (no trusted hosts, confirm on), so comparing the signals against the live
// API response is a real barrier for both fixture configurations here: one
// ships a non-empty allowlist, the other flips confirmLinkOpen to false.
async function gotoHydratedApp(page, base) {
  await stubLinkOpening(page)
  await page.goto(`${base}/`)
  await page.waitForSelector('.sess', { timeout: 5000 })
  await page.waitForFunction(async () => {
    const res = await fetch('/api/settings')
    if (!res.ok) return false
    const body = await res.json()
    const state = await import('/static/app/state.js')
    return state.confirmLinkOpenSignal.value === body.confirmLinkOpen &&
      JSON.stringify(state.trustedDomainsSignal.value) === JSON.stringify(body.trustedDomains)
  }, { timeout: 5000 })
}

// Invokes the production xterm linkHandler for `url` and reports what the
// user saw: how many confirms fired and which URLs actually opened.
async function activateLink(page, url) {
  return page.evaluate(async (target) => {
    const before = {
      confirms: window.__confirmCalls.length,
      opens: window.__openedLinks.length,
    }
    const { createTerminalLinkHandler } = await import('/static/app/terminalLinks.js')
    createTerminalLinkHandler().activate(new MouseEvent('click'), target)
    return {
      confirms: window.__confirmCalls.slice(before.confirms),
      opened: window.__openedLinks.slice(before.opens),
    }
  }, url)
}

test.describe('trusted_domains allowlist (confirm_link_open on)', () => {
  test.skip(
    ({ viewport }) => (viewport?.width || 1280) !== 1280,
    'desktop project only: the link policy is viewport-independent; one extra server is enough',
  )

  let fixture = null

  test.beforeAll(async ({}, testInfo) => {
    if ((testInfo.project.use?.viewport?.width || 1280) !== 1280) return
    fixture = await spawnFixture(['-trusted-domains', `${TRUSTED_EXACT},${TRUSTED_WILDCARD}`])
  })

  test.afterAll(() => {
    if (fixture) fixture.kill()
    fixture = null
  })

  test('GET /api/settings serves the allowlist and confirmLinkOpen:true', async ({ request }) => {
    const res = await request.get(`${fixture.base}/api/settings`)
    expect(res.status()).toBe(200)
    const body = await res.json()
    expect(body.trustedDomains).toEqual([TRUSTED_EXACT, TRUSTED_WILDCARD])
    expect(body.confirmLinkOpen).toBe(true)
  })

  test('the settings drawer shows the hydrated policy', async ({ page }) => {
    await gotoHydratedApp(page, fixture.base)
    await page.keyboard.press('Control+k')
    await expect(page.locator('[data-testid="command-palette"]')).toBeVisible()
    await page.locator('[data-testid="palette-cmd-row"]', { hasText: 'Settings drawer' }).click()
    await expect(page.locator('[data-testid="settings-panel"]')).toBeVisible({ timeout: 5000 })

    await expect(page.locator('[data-testid="settings-trusted-domains"] .v'))
      .toHaveText(`${TRUSTED_EXACT}, ${TRUSTED_WILDCARD}`)
    await expect(page.locator('[data-testid="settings-confirm-link-open"] .v')).toHaveText('on')
  })

  test('an ALLOWLISTED link opens with no confirmation', async ({ page }) => {
    await gotoHydratedApp(page, fixture.base)
    const url = `https://${TRUSTED_EXACT}/team/repo/-/merge_requests/42`
    const result = await activateLink(page, url)
    expect(result.confirms).toEqual([])
    expect(result.opened).toEqual([url])
  })

  test('a subdomain under a *. entry opens with no confirmation', async ({ page }) => {
    await gotoHydratedApp(page, fixture.base)
    const url = 'https://build7.ci.corp.example/job/9/console'
    const result = await activateLink(page, url)
    expect(result.confirms).toEqual([])
    expect(result.opened).toEqual([url])
  })

  test('a NON-allowlisted link still confirms before opening', async ({ page }) => {
    await gotoHydratedApp(page, fixture.base)
    const url = 'https://random-blog.example/post/1'
    const result = await activateLink(page, url)
    expect(result.confirms).toHaveLength(1)
    expect(result.confirms[0]).toContain(url)
    expect(result.confirms[0]).toContain('could potentially be dangerous')
    // The stub accepts, so the navigation still happens — the point is that
    // the user was asked.
    expect(result.opened).toEqual([url])
  })

  test('a lookalike host that merely ends with a trusted name still confirms', async ({ page }) => {
    await gotoHydratedApp(page, fixture.base)
    const url = `https://${TRUSTED_EXACT}.evil.test/phish`
    const result = await activateLink(page, url)
    expect(result.confirms).toHaveLength(1)
  })
})

test.describe('confirm_link_open = false (global opt-out)', () => {
  test.skip(
    ({ viewport }) => (viewport?.width || 1280) !== 1280,
    'desktop project only: the link policy is viewport-independent; one extra server is enough',
  )

  let fixture = null

  test.beforeAll(async ({}, testInfo) => {
    if ((testInfo.project.use?.viewport?.width || 1280) !== 1280) return
    fixture = await spawnFixture(['-confirm-link-open=false'])
  })

  test.afterAll(() => {
    if (fixture) fixture.kill()
    fixture = null
  })

  test('GET /api/settings reports confirmLinkOpen:false with an empty allowlist', async ({ request }) => {
    const res = await request.get(`${fixture.base}/api/settings`)
    expect(res.status()).toBe(200)
    const body = await res.json()
    expect(body.confirmLinkOpen).toBe(false)
    expect(body.trustedDomains).toEqual([])
  })

  test('every link opens without a confirmation, allowlist or not', async ({ page }) => {
    await gotoHydratedApp(page, fixture.base)
    const url = 'https://random-blog.example/post/1'
    const result = await activateLink(page, url)
    expect(result.confirms).toEqual([])
    expect(result.opened).toEqual([url])
  })
})
