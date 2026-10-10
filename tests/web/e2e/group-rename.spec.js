// e2e/group-rename.spec.js -- Issue #2555: the rename-group dialog
// (GroupNameDialog.js, mode 'rename') shipped with no way to open it.
// It now opens from `r` on a selected group, like the TUI, and from a rename
// button on the group header.
//
// Fixture seed (tests/web/fixtures/cmd/web-fixture/main.go seed()):
//   groups: work, work/innotrade, personal (collapsed)
// The fixture's RenameGroup changes the display name and keeps the path.
//
// Phone (<768px) skips: the sidebar is desktop/tablet-only.
import { test, expect } from '@playwright/test'

const dialog = (page) => page.locator('.overlay form.dialog')

test.describe('rename group (#2555)', () => {
  test.beforeEach(async ({ page, request, viewport }) => {
    test.skip(!!viewport && viewport.width < 768, 'sidebar is desktop/tablet-only')
    await request.post('/__fixture/reset')
    await page.goto('/')
    await expect(page.locator('[data-testid="group-head-work"]')).toBeVisible({ timeout: 5000 })
  })

  test('r on a selected group opens the rename dialog and renames it', async ({ page, request }) => {
    const head = page.locator('[data-testid="group-head-work"]')
    await head.locator('.name').click()
    await expect(head).toHaveClass(/\bsel\b/)

    await page.keyboard.press('r')

    await expect(dialog(page)).toBeVisible({ timeout: 2000 })
    await expect(dialog(page).locator('.kicker')).toHaveText('RENAME')
    const input = dialog(page).locator('input')
    // Prefilled with the current name, and the `r` keystroke did not leak in.
    await expect(input).toHaveValue('work')

    await input.fill('clients')
    await dialog(page).locator('button[type="submit"]').click()

    await expect(dialog(page)).toHaveCount(0)
    await expect(head.locator('.name')).toHaveText('CLIENTS', { timeout: 4000 })
    const menu = await (await request.get('/api/menu')).json()
    const g = (menu.items || []).find(i => i.type === 'group' && i.group.path === 'work')
    expect(g.group.name).toBe('clients')
    // The renamed group is still the selected one.
    await expect(head).toHaveClass(/\bsel\b/)
  })

  test('the group header rename button opens the dialog for a nested group', async ({ page, request }) => {
    const head = page.locator('[data-testid="group-head-work/innotrade"]')
    await head.hover()
    const btn = page.locator('[data-testid="group-rename-work/innotrade"]')
    await expect(btn).toBeVisible()

    const patched = page.waitForRequest(r => r.method() === 'PATCH' && r.url().includes('/api/groups/'))
    await btn.click()

    await expect(dialog(page)).toBeVisible({ timeout: 2000 })
    const input = dialog(page).locator('input')
    await expect(input).toHaveValue('innotrade')
    await input.fill('innotrade-v2')
    await dialog(page).locator('button[type="submit"]').click()

    // Per-segment encoding: the '/' between parent and child stays a '/'.
    expect(new URL((await patched).url()).pathname).toBe('/api/groups/work/innotrade')
    await expect(dialog(page)).toHaveCount(0)
    await expect(head.locator('.name')).toHaveText('INNOTRADE-V2', { timeout: 4000 })
    const menu = await (await request.get('/api/menu')).json()
    const g = (menu.items || []).find(i => i.type === 'group' && i.group.path === 'work/innotrade')
    expect(g.group.name).toBe('innotrade-v2')
  })

  test('cancel leaves the group untouched', async ({ page, request }) => {
    await page.locator('[data-testid="group-head-work"] .name').click()
    await page.keyboard.press('r')
    await expect(dialog(page)).toBeVisible({ timeout: 2000 })
    await dialog(page).locator('button', { hasText: 'Cancel' }).click()
    await expect(dialog(page)).toHaveCount(0)
    const menu = await (await request.get('/api/menu')).json()
    const g = (menu.items || []).find(i => i.type === 'group' && i.group.path === 'work')
    expect(g.group.name).toBe('work')
  })

  test('r on a session still shows the session-rename notice, not the group dialog', async ({ page }) => {
    // Keyboard focus, as keyboard-parity.spec.js does (the first j lands on
    // the `work` header): a click on tablet hands focus to the terminal.
    await page.keyboard.press('j')
    await page.keyboard.press('j')
    await page.waitForSelector('.sess.sel', { timeout: 2000 })
    await page.keyboard.press('r')
    await expect(page.locator('.toast', { hasText: /rename/i }).first()).toBeVisible({ timeout: 2000 })
    await expect(dialog(page)).toHaveCount(0)
  })
})
