import { test, expect } from '@playwright/test'

test('terminal viewport matches the surrounding frame', async ({ page, request }) => {
  await request.post('/__fixture/reset')
  await page.goto('/s/sess-001')

  const viewport = page.locator('.xterm-viewport')
  await expect(viewport).toBeVisible()
  const background = await page.locator('.term-frame').evaluate((frame) => getComputedStyle(frame).backgroundColor)
  await expect(viewport).toHaveCSS('background-color', background)
})
