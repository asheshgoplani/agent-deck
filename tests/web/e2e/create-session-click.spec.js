// e2e/create-session-click.spec.js -- clicking "Create session" right after
// typing the working dir creates the session.
//
// Before (desktop): the mousedown blurred the path field, the inline
// suggestion list collapsed, the centred dialog shrank under the pointer, the
// mouseup landed on the backdrop and the backdrop click closed the dialog:
// nothing was created and everything typed was lost.
import { test, expect } from '@playwright/test'
import { mkdtempSync, mkdirSync, rmSync } from 'node:fs'
import { tmpdir } from 'node:os'
import { join } from 'node:path'

test.use({ serviceWorkers: 'block' })

let dir
test.beforeAll(() => {
  dir = mkdtempSync(join(tmpdir(), 'web-create-click-'))
  for (const name of ['alpha', 'beta', 'gamma', 'delta', 'epsilon']) mkdirSync(join(dir, name))
})
test.afterAll(() => rmSync(dir, { recursive: true, force: true }))

test.beforeEach(async ({ page, request }, testInfo) => {
  if (testInfo.project.name === 'chromium-phone') await page.setViewportSize({ width: 390, height: 844 })
  await request.post('/__fixture/reset')
})

test('mouse click on Create session with the folder suggestions open creates the session', async ({ page }) => {
  await page.goto('/')
  await page.waitForSelector('.sess', { state: 'attached' })
  await page.keyboard.press('n')
  const dialog = page.locator('.dialog')
  await expect(dialog).toBeVisible()

  await dialog.getByPlaceholder('session-name').fill('clicked-create')
  const pathInput = page.getByRole('combobox', { name: 'Working dir' })
  await pathInput.click()
  await pathInput.pressSequentially(dir + '/')
  await expect(page.getByRole('option')).toHaveCount(5)

  const post = page.waitForRequest(r => r.method() === 'POST' && new URL(r.url()).pathname === '/api/sessions', { timeout: 4000 })
  await dialog.getByRole('button', { name: /Create session/ }).click()
  const req = await post
  expect(req.postDataJSON()).toMatchObject({ title: 'clicked-create', projectPath: dir + '/' })
  await expect(dialog).toBeHidden()
})

test('a press that starts inside the dialog and ends on the backdrop does not close it', async ({ page }) => {
  await page.goto('/')
  await page.waitForSelector('.sess', { state: 'attached' })
  await page.keyboard.press('n')
  const dialog = page.locator('.dialog')
  await expect(dialog).toBeVisible()
  await dialog.getByPlaceholder('session-name').fill('keep-me')

  const box = await dialog.locator('.dh').boundingBox()
  await page.mouse.move(box.x + 10, box.y + 10)
  await page.mouse.down()
  await page.mouse.move(2, 2)
  await page.mouse.up()

  await expect(dialog).toBeVisible()
  await expect(dialog.getByPlaceholder('session-name')).toHaveValue('keep-me')

  // A real click on the backdrop still closes it.
  await page.mouse.click(2, 2)
  await expect(dialog).toBeHidden()
})
