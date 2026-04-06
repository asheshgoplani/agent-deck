import { test, expect } from '@playwright/test'

// ThemeToggle renders three buttons: "Light", "Dark", "System"
// with title="Light theme", title="Dark theme", title="Follow system preference"

test('theme toggle switches dark/light class', async ({ page }) => {
  await page.goto('/')
  await expect(page.getByText('Sessions')).toBeVisible({ timeout: 10000 })

  const html = page.locator('html')

  // Click "Dark" theme button to ensure dark mode
  const darkBtn = page.locator('button[title="Dark theme"]')
  await darkBtn.click()
  await expect(html).toHaveClass(/dark/)

  // Click "Light" theme button to switch to light
  const lightBtn = page.locator('button[title="Light theme"]')
  await lightBtn.click()
  await expect(html).not.toHaveClass(/\bdark\b/)
})

test('theme persists across reload', async ({ page }) => {
  await page.goto('/')
  await expect(page.getByText('Sessions')).toBeVisible({ timeout: 10000 })

  // Set dark theme explicitly
  const darkBtn = page.locator('button[title="Dark theme"]')
  await darkBtn.click()

  const classAfterToggle = await page.locator('html').getAttribute('class') || ''
  const isDark = classAfterToggle.includes('dark')

  // Reload and check persistence
  await page.reload()
  await expect(page.getByText('Sessions')).toBeVisible({ timeout: 10000 })
  const classAfterReload = await page.locator('html').getAttribute('class') || ''
  const stillSame = classAfterReload.includes('dark') === isDark
  expect(stillSame).toBe(true)
})
