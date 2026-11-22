// e2e/restart-rename-help.spec.js -- the `r` key and the top tabs do what
// the UI says they do.
//
// Before:
//   - `r` on a selected session showed "use the TUI (web rename API not
//     implemented yet)" although the Edit dialog renames sessions fine, and
//     the shortcuts overlay called session rename TUI-only.
//   - With a group selected, clicking a top tab highlighted it but the
//     group panel stayed: the tab looked dead until a session was clicked.
import { test, expect } from '@playwright/test'
import { expandSeededCollapsedGroups } from '../helpers/seededSidebar.js'

test.use({ serviceWorkers: 'block' })

test.describe('r renames the focused session (desktop)', () => {
  test.skip(({ viewport }) => (viewport?.width || 1280) < 768, 'sidebar keyboard focus is desktop/tablet only')

  test('r on a selected session opens the edit dialog with its title, and saving renames it', async ({ page, request }) => {
    await request.post('/__fixture/reset')
    await expandSeededCollapsedGroups(page)
    await page.goto('/s/sess-002')
    await page.waitForSelector('.sess.sel')
    await page.locator('body').click({ position: { x: 1, y: 1 } }).catch(() => {})
    await page.keyboard.press('r')
    await expect(page.locator('.toast', { hasText: /not implemented/i })).toHaveCount(0)
    const title = page.getByTestId('edit-session-title')
    await expect(title).toBeVisible()
    await expect(title).toHaveValue('frontend')
    await title.fill('frontend-renamed')
    await page.getByRole('button', { name: /^Save/ }).click()
    await expect(page.locator('.sess.sel .tt')).toHaveText('frontend-renamed')
  })

  test('the shortcuts overlay no longer says session rename is TUI-only', async ({ page, request }) => {
    await request.post('/__fixture/reset')
    await page.goto('/')
    await page.waitForSelector('.sess')
    await page.keyboard.press('?')
    const overlay = page.getByText(/Rename focused/)
    await expect(overlay).toBeVisible()
    await expect(overlay).not.toContainText('TUI-only')
  })
})

test('with a group selected, clicking a top tab shows that tab', async ({ page, request }, testInfo) => {
  test.skip((page.viewportSize()?.width || 1280) < 768, 'top tabs are desktop/tablet only; phone uses the bottom tabs (covered below)')
  await request.post('/__fixture/reset')
  await page.goto('/g/work')
  await expect(page.getByTestId('work-head-group')).toBeVisible()
  await page.locator('.top-tab', { hasText: 'Costs' }).click()
  await expect(page.locator('.top-tab.active')).toContainText('Costs')
  await expect(page.getByTestId('work-head-group')).toHaveCount(0)
  await expect(page.locator('.costs').first()).toBeVisible()
})

test('phone: with a group selected, a bottom tab shows that tab', async ({ page, request }, testInfo) => {
  test.skip(testInfo.project.name !== 'chromium-phone', 'phone layout only')
  await page.setViewportSize({ width: 390, height: 844 })
  await request.post('/__fixture/reset')
  await page.goto('/g/work')
  await expect(page.getByTestId('work-head-group')).toBeVisible()
  await page.getByTestId('mobile-tab-costs').click()
  await expect(page.getByTestId('work-head-group')).toHaveCount(0)
  await expect(page.locator('.costs').first()).toBeVisible()
})
