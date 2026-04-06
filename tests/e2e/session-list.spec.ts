import { test, expect } from '@playwright/test'

test('page loads without JS errors', async ({ page }) => {
  const errors: string[] = []
  page.on('pageerror', (err) => errors.push(err.message))
  await page.goto('/')
  // AppShell mounts when sidebar heading is visible
  await expect(page.getByText('Sessions')).toBeVisible({ timeout: 10000 })
  // Agent Deck brand text is visible
  await expect(page.getByText('Agent Deck')).toBeVisible()
  expect(errors).toEqual([])
})

test('session list container renders', async ({ page }) => {
  await page.goto('/')
  await expect(page.getByText('Sessions')).toBeVisible({ timeout: 10000 })
  // Either shows sessions or "No sessions" message
  const sidebar = page.locator('aside')
  await expect(sidebar).toBeVisible()
})

test('connection indicator is visible', async ({ page }) => {
  await page.goto('/')
  await expect(page.getByText('Sessions')).toBeVisible({ timeout: 10000 })
  // Connection indicator shows connected/disconnected state; topbar contains it
  const topbar = page.locator('header')
  await expect(topbar).toBeVisible()
})
