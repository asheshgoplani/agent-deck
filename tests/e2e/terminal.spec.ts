import { test, expect } from '@playwright/test'

test('terminal panel area is visible in main content', async ({ page }) => {
  await page.goto('/')
  await expect(page.getByText('Sessions')).toBeVisible({ timeout: 10000 })

  // Main content area should be present
  const main = page.locator('main')
  await expect(main).toBeVisible()

  // Terminal panel div is always rendered (either shows "Select a session" or xterm)
  const terminalArea = main.locator('div').first()
  await expect(terminalArea).toBeVisible()
})

test('terminal panel is not hidden when on terminal tab', async ({ page }) => {
  await page.goto('/')
  await expect(page.getByText('Sessions')).toBeVisible({ timeout: 10000 })

  // Ensure we are on terminal tab (default: button shows "Costs")
  const costsBtn = page.locator('button[title="Cost Dashboard"]')
  await expect(costsBtn).toHaveText('Costs')

  // The terminal div is the first child of main; it should NOT have "hidden" class
  const main = page.locator('main')
  const terminalDiv = main.locator('> div').first()
  await expect(terminalDiv).toBeVisible()
  await expect(terminalDiv).not.toHaveClass(/hidden/)
})

test('terminal is hidden when costs tab is active', async ({ page }) => {
  await page.goto('/')
  await expect(page.getByText('Sessions')).toBeVisible({ timeout: 10000 })

  // Switch to costs tab
  const costsBtn = page.locator('button[title="Cost Dashboard"]')
  await costsBtn.click()
  await expect(costsBtn).toHaveText('Terminal')

  // Terminal div should now have "hidden" class (CSS hidden to preserve xterm+WS)
  const main = page.locator('main')
  const terminalDiv = main.locator('> div').first()
  await expect(terminalDiv).toHaveClass(/hidden/)
})
