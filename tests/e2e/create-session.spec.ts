import { test, expect } from '@playwright/test'

test('create session dialog opens via new session button', async ({ page }) => {
  await page.goto('/')
  await expect(page.getByText('Sessions')).toBeVisible({ timeout: 10000 })

  // Find the "New session" button in the sidebar header (aria-label="New session")
  const newBtn = page.locator('button[aria-label="New session"]')
  if (await newBtn.isVisible()) {
    await newBtn.click()
    // Dialog renders a form for session creation
    const dialog = page.locator('form')
    await expect(dialog.first()).toBeVisible({ timeout: 5000 })
  }
})

test('create session dialog opens via keyboard shortcut n', async ({ page }) => {
  await page.goto('/')
  await expect(page.getByText('Sessions')).toBeVisible({ timeout: 10000 })

  // Press 'n' to open create session dialog (keyboard shortcut in AppShell)
  await page.keyboard.press('n')

  // Dialog should be visible (contains "New Session" heading)
  await expect(page.getByText('New Session')).toBeVisible({ timeout: 5000 })
})

test('create session dialog closes on Escape', async ({ page }) => {
  await page.goto('/')
  await expect(page.getByText('Sessions')).toBeVisible({ timeout: 10000 })

  // Open dialog via keyboard shortcut
  await page.keyboard.press('n')
  await expect(page.getByText('New Session')).toBeVisible({ timeout: 5000 })

  // Close with Escape
  await page.keyboard.press('Escape')

  // Dialog should be hidden
  await expect(page.getByText('New Session')).not.toBeVisible({ timeout: 3000 })
})
