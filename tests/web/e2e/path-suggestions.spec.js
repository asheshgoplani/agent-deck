import { test, expect } from '@playwright/test'
import { mkdtempSync, mkdirSync, writeFileSync, rmSync } from 'node:fs'
import { tmpdir } from 'node:os'
import { join } from 'node:path'

// Service-worker fetches bypass page.route(), used by the async/error probes.
test.use({ serviceWorkers: 'block', hasTouch: true })

let dir
let many
test.beforeAll(() => {
  dir = mkdtempSync(join(tmpdir(), 'web-paths-'))
  for (const name of ['AirConnect', 'SpotConnect', '.hidden', 'A <img>', 'cash$PR_TRIAL_UNSET', 'cash${HOME}', 'Long' + 'x'.repeat(180)]) {
    mkdirSync(join(dir, name))
  }
  writeFileSync(join(dir, 'A-file'), 'private contents')
  many = mkdtempSync(join(tmpdir(), 'web-paths-many-'))
  for (let i = 0; i < 30; i++) mkdirSync(join(many, 'dir-' + String(i).padStart(2, '0')))
})
test.afterAll(() => {
  rmSync(dir, { recursive: true, force: true })
  rmSync(many, { recursive: true, force: true })
})

async function openDialog(page, request) {
  await request.post('/__fixture/reset')
  await page.goto('/')
  await page.waitForSelector('.sess', { state: 'attached' })
  await page.keyboard.press('n')
  await expect(page.locator('.dialog')).toBeVisible()
  return page.getByRole('combobox', { name: 'Working dir' })
}

async function installDelayedLookup(page, delayedPrefix) {
  // Deliberately ignore AbortSignal to exercise responses delivered after
  // cancellation, rather than relying on the browser to abort the request.
  await page.evaluate(delayedPrefix => {
    const original = window.fetch
    window.fetch = (url, options) => {
      if (!String(url).startsWith('/api/path-suggestions?')) return original(url, options)
      const prefix = new URL(url, location.href).searchParams.get('prefix')
      if (prefix !== delayedPrefix) return Promise.resolve(Response.json({ suggestions: [prefix + 'fresh'] }))
      window.pathLookupStarted = true
      return new Promise(resolve => {
        const response = Response.json({ suggestions: [prefix + 'stale'] })
        const json = response.json.bind(response)
        response.json = async () => {
          const data = await json()
          window.pathResponseParsed = true
          return data
        }
        window.deliverPathResponse = () => resolve(response)
      })
    }
  }, delayedPrefix)
}

async function deliverLookup(page) {
  await page.evaluate(() => window.deliverPathResponse())
  await page.waitForFunction(() => window.pathResponseParsed)
  await page.evaluate(() => new Promise(resolve => requestAnimationFrame(resolve)))
}

test('directory suggestions support arrows, Enter, Tab, Escape and mouse', async ({ page, request }) => {
  const input = await openDialog(page, request)
  await input.fill(dir + '/')
  await expect(page.getByRole('option')).toHaveCount(4)
  await expect(page.getByRole('option', { name: 'A <img>', exact: true })).toBeVisible()
  await expect(page.locator('.path-suggestions img')).toHaveCount(0)
  await input.press('ArrowDown')
  await input.press('ArrowDown')
  await expect(page.getByRole('option', { name: 'AirConnect', exact: true })).toHaveAttribute('aria-selected', 'true')
  await input.press('Enter')
  await expect(input).toHaveValue(dir + '/AirConnect/')
  await expect(page.locator('.dialog')).toBeVisible()

  await input.fill(dir + '/S')
  await expect(page.getByRole('option', { name: 'SpotConnect' })).toBeVisible()
  await input.press('Tab')
  await expect(input).toHaveValue(dir + '/SpotConnect/')
  await expect(input).toBeFocused()

  await input.fill(dir + '/')
  await expect(page.getByRole('option')).toHaveCount(4)
  await input.press('ArrowUp')
  await expect(page.getByRole('option').last()).toHaveAttribute('aria-selected', 'true')
  await input.press('Escape')
  await expect(page.getByRole('listbox')).toHaveCount(0)
  await expect(page.locator('.dialog')).toBeVisible()
  await expect(input).toHaveValue(dir + '/')

  await input.fill(dir + '/.')
  await page.getByRole('option', { name: '.hidden' }).click()
  await expect(input).toHaveValue(dir + '/.hidden/')
  await expect(input).toBeFocused()
})

test('dropdown fits long names at every viewport', async ({ page, request, viewport }) => {
  const input = await openDialog(page, request)
  await input.fill(dir + '/')
  await expect(page.getByRole('option')).toHaveCount(4)
  await expect(page.getByRole('option').last()).toBeInViewport({ ratio: 1 })
  const layout = await page.locator('.path-suggestions').evaluate(el => ({
    width: el.getBoundingClientRect().width,
    scrollWidth: el.scrollWidth,
    left: el.getBoundingClientRect().left,
    right: el.getBoundingClientRect().right,
  }))
  expect(layout.scrollWidth).toBeLessThanOrEqual(layout.width + 1)
  expect(layout.left).toBeGreaterThanOrEqual(0)
  expect(layout.right).toBeLessThanOrEqual(viewport.width)
  if (viewport.width < 400) {
    await page.screenshot({ path: '/tmp/web-paths-phone.png' })
    // Simulate the reduced viewport while a phone keyboard is visible.
    await page.setViewportSize({ width: viewport.width, height: 376 })
    await expect(page.getByRole('listbox')).toBeInViewport({ ratio: 1 })
    await expect(input).toBeInViewport({ ratio: 1 })
    await page.getByRole('option', { name: 'SpotConnect', exact: true }).tap()
    await expect(input).toHaveValue(dir + '/SpotConnect/')
  }
  await input.fill(many + '/')
  await expect(page.getByRole('option')).toHaveCount(20)
  await input.press('ArrowUp')
  await expect(page.getByRole('option').last()).toBeInViewport()
  await expect(page.getByRole('option').last()).toHaveAttribute('aria-selected', 'true')
})

test('folder rows expose pointer, touch and keyboard affordances in both themes', async ({ page, request }, testInfo) => {
  // Both theme captures and video teardown share this test's time budget.
  test.setTimeout(60_000)
  const input = await openDialog(page, request)
  await input.fill(dir + '/')
  const rows = page.getByRole('option')
  await expect(rows).toHaveCount(4)
  for (const theme of ['dark', 'light']) {
    await page.evaluate(theme => document.documentElement.dataset.theme = theme, theme)
    for (const row of await rows.all()) {
      await expect(row.locator('[aria-hidden="true"] svg')).toBeVisible()
      await expect(row).toHaveCSS('cursor', 'pointer')
      expect((await row.boundingBox()).height).toBeGreaterThanOrEqual(44)
    }
    const row = page.getByRole('option', { name: 'AirConnect', exact: true })
    await page.mouse.move(0, 0)
    const idleBackground = await row.evaluate(el => getComputedStyle(el).backgroundColor)
    await row.hover()
    await expect.poll(() => row.evaluate(el => getComputedStyle(el).backgroundColor)).not.toBe(idleBackground)
    await page.mouse.move(0, 0)
    await input.press('ArrowDown')
    await input.press('ArrowDown')
    await expect(row).toHaveAttribute('aria-selected', 'true')
    await expect(input).toHaveAttribute('aria-activedescendant', await row.getAttribute('id'))
    await expect(input).toBeFocused()
    await expect(row).toHaveCSS('outline-style', 'solid')
    const selectedBackground = await row.evaluate(el => getComputedStyle(el).backgroundColor)
    expect(selectedBackground).not.toBe(idleBackground)
    await page.screenshot({ path: '/tmp/path-polish-' + testInfo.project.name + '-' + theme + '.png' })
    await input.press('Escape')
    await input.fill(dir + '/S')
    await expect(rows).toHaveCount(1)
    await input.fill(dir + '/')
    await expect(rows).toHaveCount(4)
  }
})

test('late responses cannot replace a newer prefix', async ({ page, request }) => {
  const input = await openDialog(page, request)
  await installDelayedLookup(page, '/old/')
  await input.fill('/old/')
  await page.waitForFunction(() => window.pathLookupStarted)
  await input.fill('/new/')
  await expect(page.getByRole('option', { name: 'fresh' })).toBeVisible()
  await deliverLookup(page)
  await expect(page.getByRole('option', { name: 'fresh' })).toBeVisible()
  await expect(page.getByRole('option', { name: 'stale' })).toHaveCount(0)
  await input.press('Escape')
  await expect(page.getByRole('listbox')).toHaveCount(0)
  await expect(input).toBeFocused()
})

test('Escape dismisses a pending completion and its late response stays hidden', async ({ page, request }) => {
  const input = await openDialog(page, request)
  await installDelayedLookup(page, '/delayed/')
  await input.fill('/delayed/')
  await page.waitForFunction(() => window.pathLookupStarted)
  await input.press('Escape')
  await expect(page.locator('.dialog')).toBeVisible()
  await deliverLookup(page)
  await expect(page.getByRole('listbox')).toHaveCount(0)
  await expect(input).toHaveValue('/delayed/')
  await expect(input).toBeFocused()
  await input.press('Escape')
  await expect(page.locator('.dialog')).toHaveCount(0)
})

test('suggestions stay above a phone keyboard when only the visual viewport shrinks', async ({ page, request, viewport }) => {
  test.skip(viewport.width >= 400, 'phone keyboard layout')
  const input = await openDialog(page, request)
  await input.fill(dir + '/')
  await expect(page.getByRole('option')).toHaveCount(4)
  await page.evaluate(() => {
    Object.defineProperty(window.visualViewport, 'height', { configurable: true, get: () => 376 })
    Object.defineProperty(window.visualViewport, 'offsetTop', { configurable: true, get: () => 40 })
    window.visualViewport.dispatchEvent(new Event('resize'))
  })
  await expect.poll(() => page.locator('.dialog').evaluate(el => {
    const rect = el.getBoundingClientRect()
    return rect.top >= window.visualViewport.offsetTop && rect.bottom <= window.visualViewport.offsetTop + window.visualViewport.height
  })).toBe(true)
  await page.getByRole('option', { name: 'SpotConnect', exact: true }).tap()
  await expect(input).toHaveValue(dir + '/SpotConnect/')
})

test('blur dismisses suggestions and failed requests leave the path editable', async ({ page, request }) => {
  const input = await openDialog(page, request)
  await input.fill(dir + '/')
  await expect(page.getByRole('listbox')).toBeVisible()
  await input.press('Shift+Tab')
  await expect(page.getByRole('listbox')).toHaveCount(0)
  await page.route('**/api/path-suggestions?prefix=*', route => route.fulfill({ status: 401, json: { error: { message: 'unauthorized' } } }))
  const response = page.waitForResponse(res => res.url().includes('/api/path-suggestions?prefix=') && res.status() === 401)
  await input.fill('/manual/')
  await response
  await expect(input).toHaveValue('/manual/')
  await expect(page.getByRole('listbox')).toHaveCount(0)
})
