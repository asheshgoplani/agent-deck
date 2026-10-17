import { test, expect } from '@playwright/test'

test.use({ serviceWorkers: 'block' })

const missing = ['/srv/projects/new', '/srv/projects/new/workspace']

test.beforeEach(async ({ page, request }) => {
  await request.post('/__fixture/reset')
  await page.route('**/api/session-directory?*', route => route.fulfill({
    json: { path: missing[1], missing },
  }))
  await page.goto('/')
  await expect(page.locator('#app-root-grid')).toBeVisible()
  await page.keyboard.press('n')
  await page.locator('.dialog input').nth(1).fill(missing[1])
})

test('native consent has a full clickable target and visible keyboard focus', async ({ page }) => {
  const panel = page.locator('.dir-create-panel')
  const checkbox = page.getByRole('checkbox', { name: 'Create this folder', exact: true })
  await expect(panel).toBeVisible()
  await expect(checkbox).not.toBeChecked()
  expect(await checkbox.evaluate(el => el.tagName === 'INPUT' && el.type === 'checkbox')).toBe(true)
  const label = panel.locator('.dir-create-choice')
  expect((await label.boundingBox()).height).toBeGreaterThanOrEqual(44)
  expect(await label.evaluate(el => getComputedStyle(el).cursor)).toBe('pointer')
  expect(await checkbox.evaluate(el => getComputedStyle(el).cursor)).toBe('pointer')
  await label.getByText('Create this folder', { exact: true }).click()
  await expect(checkbox).toBeChecked()
  await page.locator('.dialog input').nth(1).focus()
  await page.keyboard.press('Tab')
  await expect(checkbox).toBeFocused()
  await page.keyboard.press('Space')
  await expect(checkbox).not.toBeChecked()
  expect(await checkbox.evaluate(el => el.matches(':focus-visible'))).toBe(true)
  expect(await checkbox.evaluate(el => getComputedStyle(el).outlineStyle)).toBe('solid')
  expect(await checkbox.evaluate(el => parseFloat(getComputedStyle(el).outlineWidth))).toBeGreaterThanOrEqual(2)
  await expect(panel.locator('.dir-create-path')).toHaveText(missing)
  await expect(panel.locator('.dir-create-path-icon svg')).toHaveCount(2)
  await expect(panel.locator('.dir-create-path-icon').first()).toHaveAttribute('aria-hidden', 'true')
  await panel.screenshot({ path: test.info().outputPath('consent-keyboard-focus.png') })
  await page.locator('.dialog').screenshot({ path: test.info().outputPath('consent-dialog-focus.png') })
})

test('full long paths wrap inside the panel in both themes', async ({ page }) => {
  const longPath = '/srv/projects/' + 'longprojectdirectorynamewithoutseparators'.repeat(6)
  await page.unroute('**/api/session-directory?*')
  await page.route('**/api/session-directory?*', route => route.fulfill({
    json: { path: longPath + '/workspace', missing: [longPath, longPath + '/workspace'] },
  }))
  await page.locator('.dialog input').nth(1).fill(longPath + '/workspace')
  const panel = page.locator('.dir-create-panel')
  await expect(panel.locator('.dir-create-path')).toHaveText([longPath, longPath + '/workspace'])
  for (const theme of ['dark', 'light']) {
    await page.evaluate(value => document.documentElement.dataset.theme = value, theme)
    expect(await panel.evaluate(el => el.scrollWidth <= el.clientWidth)).toBe(true)
    const path = panel.locator('.dir-create-path').first()
    expect(await path.evaluate(el => el.scrollHeight > parseFloat(getComputedStyle(el).lineHeight))).toBe(true)
    const box = await panel.boundingBox()
    expect(box.x + box.width).toBeLessThanOrEqual(page.viewportSize().width)
    await panel.screenshot({ path: test.info().outputPath('consent-long-path-' + theme + '.png') })
  }
})
