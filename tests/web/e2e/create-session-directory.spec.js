import { test, expect } from '@playwright/test'
import { mkdtempSync, mkdirSync, existsSync, readdirSync, rmSync, writeFileSync } from 'node:fs'
import { tmpdir } from 'node:os'
import { join } from 'node:path'

// Service-worker fetches bypass Playwright's delayed-response route below.
test.use({ serviceWorkers: 'block' })

let root
let path

test.beforeEach(async ({ page, request }) => {
  root = mkdtempSync(join(tmpdir(), 'session-directory-'))
  path = join(root, 'parent', 'project')
  await request.post('/__fixture/reset')
  await page.goto('/')
  await expect(page.locator('#app-root-grid')).toBeVisible()
  await page.keyboard.press('n')
  await expect(page.locator('.dialog')).toBeVisible()
  await page.locator('.dialog input').first().fill('scratch')
  await page.locator('.dialog input').nth(1).fill(path)
})

test.afterEach(() => rmSync(root, { recursive: true, force: true }))

test('previews every missing parent without creating anything', async ({ page }) => {
  const checkbox = page.getByRole('checkbox', { name: 'Create this folder' })
  await expect(checkbox).toBeVisible()
  await expect(checkbox).not.toBeChecked()
  await expect(page.getByText('Folder does not exist.', { exact: true })).toBeVisible()
  await expect(page.locator('.dialog li')).toHaveText([join(root, 'parent'), path])
  expect(existsSync(join(root, 'parent'))).toBe(false)
})

test('creates only with opt-in and sends the approved directory list', async ({ page }) => {
  await page.getByRole('checkbox', { name: 'Create this folder' }).check()
  const posted = page.waitForRequest(req => req.url().endsWith('/api/sessions') && req.method() === 'POST')
  await page.locator('.dialog button[type=submit]').click()
  expect((await posted).postDataJSON()).toMatchObject({ createDir: true, projectPath: path, createDirPaths: [join(root, 'parent'), path] })
  await expect(page.locator('.dialog')).toHaveCount(0)
  expect(readdirSync(path)).toEqual([])
})

test('keeps the legacy request and leaves the directory missing without opt-in', async ({ page }) => {
  await expect(page.getByRole('checkbox', { name: 'Create this folder' })).toBeVisible()
  const posted = page.waitForRequest(req => req.url().endsWith('/api/sessions') && req.method() === 'POST')
  await page.locator('.dialog button[type=submit]').click()
  expect((await posted).postDataJSON()).not.toHaveProperty('createDir')
  expect(existsSync(join(root, 'parent'))).toBe(false)
})

test('clears consent on path edits and ignores stale previews', async ({ page }) => {
  await page.getByRole('checkbox', { name: 'Create this folder' }).check()
  let releaseOld
  let oldStarted
  const started = new Promise(resolve => { oldStarted = resolve })
  const oldPath = join(root, 'old')
  await page.route('**/api/session-directory?*', async route => {
    if (new URL(route.request().url()).searchParams.get('path') !== oldPath) return route.continue()
    oldStarted()
    await new Promise(resolve => { releaseOld = resolve })
    await route.fulfill({ json: { path: oldPath, missing: [oldPath] } })
  })
  await page.locator('.dialog input').nth(1).fill(oldPath)
  await expect(page.getByRole('checkbox')).toHaveCount(0)
  await started
  const nextPath = join(root, 'other')
  await page.locator('.dialog input').nth(1).fill(nextPath)
  await expect(page.getByRole('checkbox', { name: 'Create this folder' })).not.toBeChecked()
  releaseOld()
  await expect(page.locator('.dialog li')).toHaveText([nextPath])
})

test('reports a file blocking the directory and offers no creation', async ({ page }) => {
  writeFileSync(join(root, 'file'), '')
  await page.locator('.dialog input').nth(1).fill(join(root, 'file'))
  await expect(page.getByText(/path is not a directory/)).toBeVisible()
  await expect(page.getByRole('checkbox')).toHaveCount(0)
})

test('does not offer creation for an existing directory', async ({ page }) => {
  const checked = page.waitForResponse(res => res.url().includes('/api/session-directory?') && new URL(res.url()).searchParams.get('path') === root)
  await page.locator('.dialog input').nth(1).fill(root)
  expect((await checked).status()).toBe(200)
  await expect(page.getByRole('checkbox')).toHaveCount(0)
})

test('requires renewed consent when another parent disappears after preview', async ({ page }) => {
  mkdirSync(join(root, 'parent'))
  await expect(page.locator('.dialog li')).toHaveText([path])
  await page.getByRole('checkbox', { name: 'Create this folder' }).check()
  rmSync(join(root, 'parent'), { recursive: true })
  await page.locator('.dialog button[type=submit]').click()
  await expect(page.locator('.dialog').getByText(/directory list changed/)).toBeVisible()
  await expect(page.locator('.dialog li')).toHaveText([join(root, 'parent'), path])
  await expect(page.getByRole('checkbox', { name: 'Create this folder' })).not.toBeChecked()
  expect(existsSync(join(root, 'parent'))).toBe(false)
})
