import { test, expect } from '@playwright/test'
import {
  mockAllEndpoints,
  mockSessionCRUD,
  createTestState,
  waitForAppReady,
  resetIdCounter,
} from './helpers/test-fixtures'

test.describe('Session lifecycle E2E', () => {
  let state: ReturnType<typeof createTestState>

  test.beforeEach(async ({ page }) => {
    resetIdCounter()
    state = createTestState()
    await mockAllEndpoints(page)
    await mockSessionCRUD(page, state)
  })

  test('create session via dialog and verify it appears in sidebar', async ({ page }) => {
    await page.goto('/?token=test')
    await waitForAppReady(page)
    await page.waitForSelector('#preact-session-list', { state: 'attached', timeout: 10000 })

    // Open the create session dialog
    const newBtn = page.locator('button[aria-label="New session"]')
    await newBtn.click()
    await expect(page.getByRole('heading', { name: 'New Session' })).toBeVisible({ timeout: 5000 })

    // Fill the form: title is the first input, path is the second
    const form = page.locator('form')
    const inputs = form.locator('input')
    await inputs.nth(0).fill('E2E Test Session')
    await inputs.nth(1).fill('/tmp/e2e-test')

    // Submit the form
    await form.locator('button[type="submit"]').click()

    // Dialog should close
    await expect(page.getByRole('heading', { name: 'New Session' })).not.toBeVisible({ timeout: 5000 })

    // Reload to pick up the mock's updated menu state
    await page.goto('/?token=test')
    await waitForAppReady(page)
    await page.waitForSelector('#preact-session-list', { state: 'attached', timeout: 10000 })

    // Verify the new session appears in the sidebar
    await expect(page.locator('#preact-session-list').getByText('E2E Test Session')).toBeVisible({ timeout: 5000 })
  })

  test('select a session and verify terminal panel area is visible', async ({ page }) => {
    await page.goto('/?token=test')
    await waitForAppReady(page)
    await page.waitForSelector('#preact-session-list', { state: 'attached', timeout: 10000 })

    // Click the first session row.
    // Use dispatchEvent because the outer button contains nested toolbar buttons —
    // Playwright's mouse-simulation click doesn't reliably trigger Preact's onClick
    // on the outer button (nested interactive element HTML quirk). dispatchEvent
    // fires the synthetic click event directly into Preact's event system.
    const sessionRow = page.locator('#preact-session-list button[data-session-id="sess-001"]')
    await sessionRow.dispatchEvent('click')
    await page.waitForTimeout(200)

    // Verify it becomes the current selection
    await expect(sessionRow).toHaveAttribute('aria-current', 'true')

    // Main content area should be visible
    const main = page.locator('main')
    await expect(main).toBeVisible()

    // The terminal area div (first child of main) should be visible and not hidden
    const terminalDiv = main.locator('> div').first()
    await expect(terminalDiv).toBeVisible()
    await expect(terminalDiv).not.toHaveClass(/hidden/)
  })

  test('stop a running session and verify status changes', async ({ page }) => {
    await page.goto('/?token=test')
    await waitForAppReady(page)
    await page.waitForSelector('#preact-session-list', { state: 'attached', timeout: 10000 })

    // Session sess-001 is 'running' (has animate-pulse on its status dot)
    const sessionRow = page.locator('#preact-session-list button[data-session-id="sess-001"]')
    await sessionRow.hover()

    // Click Stop button
    const stopBtn = sessionRow.locator('button[aria-label="Stop session"]')
    await expect(stopBtn).toBeVisible({ timeout: 3000 })
    await stopBtn.click()

    // The mock immediately updates the session status to 'stopped'.
    // SessionRow uses optimistic UI with a 3s timer, but we can also reload
    // to pick up the real mock state.
    await page.goto('/?token=test')
    await waitForAppReady(page)
    await page.waitForSelector('#preact-session-list', { state: 'attached', timeout: 10000 })

    // After stop, the session row should exist. Its status dot should NOT
    // have animate-pulse (running/waiting pulse, stopped/idle/error do not).
    const updatedRow = page.locator('#preact-session-list button[data-session-id="sess-001"]')
    await expect(updatedRow).toBeVisible()
    const statusDot = updatedRow.locator('span.rounded-full').first()
    await expect(statusDot).not.toHaveClass(/animate-pulse/)
  })

  test('delete a session via confirm dialog and verify removal', async ({ page }) => {
    await page.goto('/?token=test')
    await waitForAppReady(page)
    await page.waitForSelector('#preact-session-list', { state: 'attached', timeout: 10000 })

    // Target the idle session (sess-003) for deletion
    const sessionRow = page.locator('#preact-session-list button[data-session-id="sess-003"]')
    await sessionRow.hover()

    // Click Delete button
    const deleteBtn = sessionRow.locator('button[aria-label="Delete session"]')
    await expect(deleteBtn).toBeVisible({ timeout: 3000 })
    await deleteBtn.click()

    // Confirm dialog should appear
    const confirmDialog = page.locator('.fixed.inset-0.z-50.bg-black\\/50')
    await expect(confirmDialog).toBeVisible({ timeout: 5000 })

    // Dialog should mention the session title
    await expect(confirmDialog).toContainText('Blog drafts')

    // Click the Delete button in the confirm dialog
    await confirmDialog.getByRole('button', { name: 'Delete', exact: true }).click()

    // Reload to pick up mock's updated state
    await page.goto('/?token=test')
    await waitForAppReady(page)
    await page.waitForSelector('#preact-session-list', { state: 'attached', timeout: 10000 })

    // Session should be gone from sidebar
    await expect(page.locator('#preact-session-list button[data-session-id="sess-003"]')).toHaveCount(0)
  })

  test('full lifecycle: create, select, stop, delete in sequence', async ({ page }) => {
    await page.goto('/?token=test')
    await waitForAppReady(page)
    await page.waitForSelector('#preact-session-list', { state: 'attached', timeout: 10000 })

    // --- CREATE ---
    await page.locator('button[aria-label="New session"]').click()
    await expect(page.getByRole('heading', { name: 'New Session' })).toBeVisible({ timeout: 5000 })
    const form = page.locator('form')
    await form.locator('input').nth(0).fill('Lifecycle Test')
    await form.locator('input').nth(1).fill('/tmp/lifecycle')
    await form.locator('button[type="submit"]').click()
    await expect(page.getByRole('heading', { name: 'New Session' })).not.toBeVisible({ timeout: 5000 })

    // Reload to pick up the new session
    await page.goto('/?token=test')
    await waitForAppReady(page)
    await page.waitForSelector('#preact-session-list', { state: 'attached', timeout: 10000 })
    const newSessionText = page.locator('#preact-session-list').getByText('Lifecycle Test')
    await expect(newSessionText).toBeVisible({ timeout: 5000 })

    // Find the new session's data-session-id (it was generated by the mock)
    // The new session row is the one containing "Lifecycle Test"
    const newRow = page.locator('#preact-session-list button[data-session-id]', {
      has: page.getByText('Lifecycle Test'),
    })
    const newSessionId = await newRow.getAttribute('data-session-id')
    expect(newSessionId).toBeTruthy()

    // --- SELECT ---
    // Use dispatchEvent for same reason as the select test (outer button + nested toolbar)
    await newRow.dispatchEvent('click')
    await page.waitForTimeout(200)
    await expect(newRow).toHaveAttribute('aria-current', 'true')
    const main = page.locator('main')
    await expect(main).toBeVisible()

    // --- STOP ---
    await newRow.hover()
    const stopBtn = newRow.locator('button[aria-label="Stop session"]')
    await expect(stopBtn).toBeVisible({ timeout: 3000 })
    await stopBtn.click()

    // Reload to pick up stopped status
    await page.goto('/?token=test')
    await waitForAppReady(page)
    await page.waitForSelector('#preact-session-list', { state: 'attached', timeout: 10000 })
    const stoppedRow = page.locator(`#preact-session-list button[data-session-id="${newSessionId}"]`)
    await expect(stoppedRow).toBeVisible()
    const statusDot = stoppedRow.locator('span.rounded-full').first()
    await expect(statusDot).not.toHaveClass(/animate-pulse/)

    // --- DELETE ---
    await stoppedRow.hover()
    // After stop, the row shows Restart instead of Stop. We can delete from
    // the idle/stopped/error state action set.
    const deleteBtn = stoppedRow.locator('button[aria-label="Delete session"]')
    await expect(deleteBtn).toBeVisible({ timeout: 3000 })
    await deleteBtn.click()

    // Confirm dialog
    const confirmDialog = page.locator('.fixed.inset-0.z-50.bg-black\\/50')
    await expect(confirmDialog).toBeVisible({ timeout: 5000 })
    await confirmDialog.getByRole('button', { name: 'Delete', exact: true }).click()

    // Reload to verify removal
    await page.goto('/?token=test')
    await waitForAppReady(page)
    await page.waitForSelector('#preact-session-list', { state: 'attached', timeout: 10000 })
    await expect(page.locator(`#preact-session-list button[data-session-id="${newSessionId}"]`)).toHaveCount(0)
  })
})
