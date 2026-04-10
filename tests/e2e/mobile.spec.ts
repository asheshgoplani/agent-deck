import { test, expect } from '@playwright/test'

test.use({ viewport: { width: 375, height: 667 } })

test('mobile layout shows topbar', async ({ page }) => {
  await page.goto('/')
  // On mobile the app loads; topbar "Agent Deck" brand is always visible
  await expect(page.getByText('Agent Deck')).toBeVisible({ timeout: 10000 })

  // Topbar header should be visible
  const topbar = page.locator('header')
  await expect(topbar).toBeVisible()
})

test('mobile layout has no horizontal overflow', async ({ page }) => {
  await page.goto('/')
  await expect(page.getByText('Agent Deck')).toBeVisible({ timeout: 10000 })

  // Check no horizontal scroll (1px tolerance for sub-pixel rendering)
  const scrollWidth = await page.evaluate(() => document.documentElement.scrollWidth)
  const clientWidth = await page.evaluate(() => document.documentElement.clientWidth)
  expect(scrollWidth).toBeLessThanOrEqual(clientWidth + 1)
})

test('mobile has no JS errors on load', async ({ page }) => {
  const errors: string[] = []
  page.on('pageerror', (err) => errors.push(err.message))
  await page.goto('/')
  await expect(page.getByText('Agent Deck')).toBeVisible({ timeout: 10000 })
  expect(errors).toEqual([])
})
