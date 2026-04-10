import { test, expect } from '@playwright/test'
import {
  mockAllEndpoints,
  mockGroupCRUD,
  createTestState,
  waitForAppReady,
  resetIdCounter,
} from './helpers/test-fixtures'

test.describe('Group CRUD E2E', () => {
  let state: ReturnType<typeof createTestState>

  test.beforeEach(async ({ page }) => {
    resetIdCounter()
    state = createTestState()
    await mockAllEndpoints(page)
    await mockGroupCRUD(page, state)
  })

  test('create a group via dialog and verify it appears in sidebar', async ({ page }) => {
    await page.goto('/?token=test')
    await waitForAppReady(page)
    await page.waitForSelector('#preact-session-list', { state: 'attached', timeout: 10000 })

    // Click the "New group" button in the sidebar header
    const newGroupBtn = page.locator('button[aria-label="New group"]')
    await newGroupBtn.click()

    // GroupNameDialog should appear with "New Group" heading
    await expect(page.getByRole('heading', { name: 'New Group' })).toBeVisible({ timeout: 5000 })

    // Fill the group name
    const dialog = page.locator('.fixed.inset-0.z-50.bg-black\\/50')
    await dialog.locator('input').fill('E2E Test Group')

    // Submit
    await dialog.locator('button[type="submit"]').click()

    // Dialog should close
    await expect(page.getByRole('heading', { name: 'New Group' })).not.toBeVisible({ timeout: 5000 })

    // Reload to pick up mock's updated menu
    await page.goto('/?token=test')
    await waitForAppReady(page)
    await page.waitForSelector('#preact-session-list', { state: 'attached', timeout: 10000 })

    // Verify the new group appears
    await expect(page.locator('#preact-session-list').getByText('E2E Test Group')).toBeVisible({ timeout: 5000 })
  })

  test('rename a group and verify the new name appears', async ({ page }) => {
    await page.goto('/?token=test')
    await waitForAppReady(page)
    await page.waitForSelector('#preact-session-list', { state: 'attached', timeout: 10000 })

    // Find the "Work" group row (button with aria-expanded that contains "Work")
    const workGroup = page.locator('#preact-session-list button[aria-expanded]', {
      hasText: 'Work',
    }).first()
    await expect(workGroup).toBeVisible()
    await workGroup.hover()

    // Click the Rename button
    const renameBtn = workGroup.locator('button[aria-label="Rename group"]')
    await expect(renameBtn).toBeVisible({ timeout: 3000 })
    await renameBtn.click()

    // Rename dialog should appear
    await expect(page.getByText('Rename Group')).toBeVisible({ timeout: 5000 })

    // Clear existing name and type new name
    const dialog = page.locator('.fixed.inset-0.z-50.bg-black\\/50')
    const nameInput = dialog.locator('input')
    await nameInput.clear()
    await nameInput.fill('Engineering')

    // Submit
    await dialog.locator('button[type="submit"]').click()
    await expect(page.getByText('Rename Group')).not.toBeVisible({ timeout: 5000 })

    // Reload to pick up mock's updated menu
    await page.goto('/?token=test')
    await waitForAppReady(page)
    await page.waitForSelector('#preact-session-list', { state: 'attached', timeout: 10000 })

    // Verify the renamed group appears
    await expect(
      page.locator('#preact-session-list button[aria-expanded]', { hasText: 'Engineering' }).first()
    ).toBeVisible({ timeout: 5000 })
  })

  test('delete a group via confirm dialog and verify removal', async ({ page }) => {
    await page.goto('/?token=test')
    await waitForAppReady(page)
    await page.waitForSelector('#preact-session-list', { state: 'attached', timeout: 10000 })

    // Find the "Personal" group row
    const personalGroup = page.locator('#preact-session-list button[aria-expanded]', {
      hasText: 'Personal',
    }).first()
    await expect(personalGroup).toBeVisible()
    await personalGroup.hover()

    // Click the Delete button
    const deleteBtn = personalGroup.locator('button[aria-label="Delete group"]')
    await expect(deleteBtn).toBeVisible({ timeout: 3000 })
    await deleteBtn.click()

    // Confirm dialog should appear
    const confirmDialog = page.locator('.fixed.inset-0.z-50.bg-black\\/50')
    await expect(confirmDialog).toBeVisible({ timeout: 5000 })
    await expect(confirmDialog).toContainText('Personal')

    // Click Delete in the confirm dialog
    await confirmDialog.getByRole('button', { name: 'Delete', exact: true }).click()

    // Reload to verify removal
    await page.goto('/?token=test')
    await waitForAppReady(page)
    await page.waitForSelector('#preact-session-list', { state: 'attached', timeout: 10000 })

    // Verify "Personal" group is gone (no group button with that text)
    await expect(
      page.locator('#preact-session-list button[aria-expanded]', { hasText: 'Personal' })
    ).toHaveCount(0)
  })

  test('full group lifecycle: create, rename, delete in sequence', async ({ page }) => {
    await page.goto('/?token=test')
    await waitForAppReady(page)
    await page.waitForSelector('#preact-session-list', { state: 'attached', timeout: 10000 })

    // --- CREATE ---
    await page.locator('button[aria-label="New group"]').click()
    await expect(page.getByRole('heading', { name: 'New Group' })).toBeVisible({ timeout: 5000 })
    const createDialog = page.locator('.fixed.inset-0.z-50.bg-black\\/50')
    await createDialog.locator('input').fill('Temporary Group')
    await createDialog.locator('button[type="submit"]').click()
    await expect(page.getByRole('heading', { name: 'New Group' })).not.toBeVisible({ timeout: 5000 })

    // Reload to pick up the new group
    await page.goto('/?token=test')
    await waitForAppReady(page)
    await page.waitForSelector('#preact-session-list', { state: 'attached', timeout: 10000 })
    const newGroup = page.locator('#preact-session-list button[aria-expanded]', {
      hasText: 'Temporary Group',
    }).first()
    await expect(newGroup).toBeVisible({ timeout: 5000 })

    // --- RENAME ---
    await newGroup.hover()
    const renameBtn = newGroup.locator('button[aria-label="Rename group"]')
    await expect(renameBtn).toBeVisible({ timeout: 3000 })
    await renameBtn.click()
    await expect(page.getByText('Rename Group')).toBeVisible({ timeout: 5000 })
    const renameDialog = page.locator('.fixed.inset-0.z-50.bg-black\\/50')
    const renameInput = renameDialog.locator('input')
    await renameInput.clear()
    await renameInput.fill('Renamed Group')
    await renameDialog.locator('button[type="submit"]').click()
    await expect(page.getByText('Rename Group')).not.toBeVisible({ timeout: 5000 })

    // Reload to pick up the rename
    await page.goto('/?token=test')
    await waitForAppReady(page)
    await page.waitForSelector('#preact-session-list', { state: 'attached', timeout: 10000 })
    const renamedGroup = page.locator('#preact-session-list button[aria-expanded]', {
      hasText: 'Renamed Group',
    }).first()
    await expect(renamedGroup).toBeVisible({ timeout: 5000 })

    // --- DELETE ---
    await renamedGroup.hover()
    const deleteBtn = renamedGroup.locator('button[aria-label="Delete group"]')
    await expect(deleteBtn).toBeVisible({ timeout: 3000 })
    await deleteBtn.click()
    const confirmDialog = page.locator('.fixed.inset-0.z-50.bg-black\\/50')
    await expect(confirmDialog).toBeVisible({ timeout: 5000 })
    await confirmDialog.getByRole('button', { name: 'Delete', exact: true }).click()

    // Reload to verify removal
    await page.goto('/?token=test')
    await waitForAppReady(page)
    await page.waitForSelector('#preact-session-list', { state: 'attached', timeout: 10000 })
    await expect(
      page.locator('#preact-session-list button[aria-expanded]', { hasText: 'Renamed Group' })
    ).toHaveCount(0)
    await expect(
      page.locator('#preact-session-list button[aria-expanded]', { hasText: 'Temporary Group' })
    ).toHaveCount(0)
  })
})
