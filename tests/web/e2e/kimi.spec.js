import { test, expect } from '@playwright/test'

test.describe('Kimi web session creation', () => {
  test.use({ serviceWorkers: 'block' })
  test.skip(({ viewport }) => (viewport?.width || 1280) < 768, 'phone viewport: picker UI is desktop/tablet only')

  test.beforeEach(async ({ request }) => {
    const res = await request.post('/__fixture/reset')
    expect(res.status()).toBe(204)
  })

  test('fallback picker submits the selected model and Always Ask permission override', async ({ page }) => {
    await page.route('**/api/settings', async (route) => {
      const response = await route.fetch()
      const body = await response.json()
      body.pickerTools = []
      body.modelCatalog = {}
      await route.fulfill({ response, json: body })
    })

    let posted = null
    await page.route('**/api/sessions', async (route) => {
      if (route.request().method() !== 'POST') return route.continue()
      posted = route.request().postDataJSON()
      await route.fulfill({
        status: 201,
        contentType: 'application/json',
        body: JSON.stringify({ sessionId: 'kimi-browser-id' }),
      })
    })

    await page.goto('/')
    await page.waitForSelector('.sess', { timeout: 5000 })
    await page.locator('[data-testid="group-head-work"] .name').click()
    await page.keyboard.press('n')

    const dialog = page.locator('.overlay .dialog')
    await expect(dialog).toBeVisible()
    const kimiButton = dialog.locator('.seg-btn').filter({ hasText: /^kimi$/ })
    await expect(kimiButton).toBeVisible()
    await kimiButton.click()
    await expect(kimiButton).toHaveClass(/on/)

    const modelSelect = dialog.locator('select').first()
    await expect(modelSelect).toBeVisible()
    await modelSelect.selectOption('kimi-code/kimi-for-coding')
    await expect(modelSelect).toHaveValue('kimi-code/kimi-for-coding')
    const permissionSelect = dialog.locator('select[aria-label="Kimi permission mode"]')
    await expect(permissionSelect).toBeVisible()
    await permissionSelect.selectOption('always-ask')
    await expect(permissionSelect).toHaveValue('always-ask')
    await dialog.locator('input[placeholder="session-name"]').fill('kimi-browser')
    await dialog.locator('button[type=submit]').click()
    await expect(dialog).toHaveCount(0)

    expect(posted).toEqual({
      title: 'kimi-browser',
      tool: 'kimi',
      projectPath: '/srv/work',
      groupPath: 'work',
      modelId: 'kimi-code/kimi-for-coding',
      kimiPermissionMode: 'always-ask',
    })
  })
})
