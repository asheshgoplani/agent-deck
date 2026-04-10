import { test, expect } from '@playwright/test'

test('costs tab button toggles to cost dashboard', async ({ page }) => {
  await page.goto('/')
  await expect(page.getByText('Sessions')).toBeVisible({ timeout: 10000 })

  // Find costs button in topbar (title="Cost Dashboard", initially shows "Costs")
  const costsBtn = page.locator('button[title="Cost Dashboard"]')
  await expect(costsBtn).toBeVisible()
  await expect(costsBtn).toHaveText('Costs')

  // Click to switch to costs tab
  await costsBtn.click()

  // Button text should change to "Terminal"
  await expect(costsBtn).toHaveText('Terminal')

  // Main area should be visible (cost dashboard renders or shows error/loading)
  const mainArea = page.locator('main')
  await expect(mainArea).toBeVisible()
})

test('costs tab switches back to terminal', async ({ page }) => {
  await page.goto('/')
  await expect(page.getByText('Sessions')).toBeVisible({ timeout: 10000 })

  const costsBtn = page.locator('button[title="Cost Dashboard"]')
  // Switch to costs
  await costsBtn.click()
  await expect(costsBtn).toHaveText('Terminal')

  // Switch back to terminal
  await costsBtn.click()
  await expect(costsBtn).toHaveText('Costs')
})
