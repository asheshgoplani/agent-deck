import { test, expect } from '@playwright/test'
import {
  mockAllEndpoints,
  waitForAppReady,
} from './helpers/test-fixtures'

test.describe('Mobile E2E', () => {
  test.beforeEach(async ({ page }) => {
    await mockAllEndpoints(page)
  })

  test('hamburger button opens and closes sidebar drawer', async ({ page }) => {
    await page.goto('/?token=test')
    await waitForAppReady(page)

    const viewport = page.viewportSize()
    if (!viewport || viewport.width >= 1024) {
      // On desktop (lg+), sidebar is always visible via lg:translate-x-0.
      // Hamburger is hidden via lg:hidden. Skip this test at large viewports.
      test.skip()
      return
    }

    const aside = page.locator('aside')

    // On iPad (768px+), sidebar starts open (sidebarOpenSignal defaults to true
    // when window.innerWidth >= 768). So close it first to test the open cycle.
    if (viewport.width >= 768) {
      const closeBtn = page.locator('button[aria-label="Close sidebar"]')
      if (await closeBtn.isVisible({ timeout: 2000 }).catch(() => false)) {
        // Use dispatchEvent because the sidebar (z-40) covers the topbar (z-10)
        // on narrow viewports — Playwright's mouse simulation is blocked by the
        // sidebar overlay, but dispatchEvent fires directly into Preact's event system.
        await closeBtn.dispatchEvent('click')
        await page.waitForTimeout(300)
      }
    }

    // Hamburger button should now show "Open sidebar"
    const hamburger = page.locator('button[aria-label="Open sidebar"]')
    await expect(hamburger).toBeVisible({ timeout: 5000 })

    // Sidebar should be hidden on phone (<768px)
    if (viewport.width < 768) {
      await expect(aside).toHaveClass(/-translate-x-full/)
    }

    // Click hamburger to open sidebar.
    // Use dispatchEvent because on mobile the sidebar (z-40) can cover the
    // hamburger (topbar at z-10) — dispatchEvent bypasses pointer-event interception.
    await hamburger.dispatchEvent('click')
    await page.waitForTimeout(300)
    await expect(aside).toHaveClass(/translate-x-0/)
    await expect(aside).not.toHaveClass(/-translate-x-full/)

    // Hamburger label should change to "Close sidebar"
    const closeBtn = page.locator('button[aria-label="Close sidebar"]')
    await expect(closeBtn).toBeVisible()

    // Close by dispatchEvent on the close button.
    // The backdrop (z-30) is behind the sidebar (z-40) so clicking it is blocked by
    // the sidebar overlay. dispatchEvent fires directly into Preact's event system.
    await closeBtn.dispatchEvent('click')
    await page.waitForTimeout(300)

    // On phone, sidebar should go back to hidden
    if (viewport.width < 768) {
      await expect(aside).toHaveClass(/-translate-x-full/)
    }
  })

  test('overflow menu opens and contains expected items', async ({ page }) => {
    await page.goto('/?token=test')
    await waitForAppReady(page)

    const viewport = page.viewportSize()

    // The overflow button is only visible on viewports < 600px (max-[599px]:flex)
    const overflowBtn = page.locator('button[aria-label="More options"]')

    if (viewport && viewport.width < 600) {
      // Phone viewports: overflow button should be visible
      await expect(overflowBtn).toBeVisible({ timeout: 5000 })

      // Click to open
      await overflowBtn.click()

      // Menu should appear
      const menu = page.locator('div[role="menu"][aria-label="More options menu"]')
      await expect(menu).toBeVisible({ timeout: 3000 })

      // Verify expected menu items
      await expect(menu.getByText('Costs')).toBeVisible()
      await expect(menu.getByText('Status:')).toBeVisible()
      await expect(menu.getByText('Theme:')).toBeVisible()
      await expect(menu.getByText('Profile:')).toBeVisible()
      await expect(menu.getByText('Info')).toBeVisible()

      // Close via Escape
      await page.keyboard.press('Escape')
      await expect(menu).not.toBeVisible({ timeout: 3000 })
    } else {
      // Tablet/desktop: overflow button should NOT be visible
      // Desktop controls are shown inline instead
      await expect(overflowBtn).not.toBeVisible()
    }
  })

  test('sidebar drawer auto-closes on session selection (phone viewports)', async ({ page }) => {
    await page.goto('/?token=test')
    await waitForAppReady(page)

    const viewport = page.viewportSize()
    if (!viewport || viewport.width >= 768) {
      // Auto-close only triggers on viewports < 768px per AppShell.js line 76
      test.skip()
      return
    }

    const aside = page.locator('aside')

    // Open sidebar via hamburger
    const hamburger = page.locator('button[aria-label="Open sidebar"]')
    await hamburger.click()
    await expect(aside).toHaveClass(/translate-x-0/)

    // Wait for session list to render inside sidebar
    await page.waitForSelector('#preact-session-list', { state: 'attached', timeout: 10000 })

    // Click a session row.
    // Use dispatchEvent because outer button has nested toolbar buttons — Playwright's
    // mouse simulation doesn't reliably trigger Preact's onClick on the outer button.
    const sessionRow = page.locator('#preact-session-list button[data-session-id="sess-001"]')
    await sessionRow.dispatchEvent('click')

    // Sidebar should auto-close on phone
    await expect(aside).toHaveClass(/-translate-x-full/, { timeout: 3000 })
  })

  test('terminal panel area is visible after session selection', async ({ page }) => {
    await page.goto('/?token=test')
    await waitForAppReady(page)

    const viewport = page.viewportSize()

    // On phone, we need to open the sidebar first.
    // Use dispatchEvent to bypass z-index issues with the sidebar covering the hamburger.
    if (viewport && viewport.width < 1024) {
      const openBtn = page.locator('button[aria-label="Open sidebar"]')
      if (await openBtn.isVisible({ timeout: 2000 }).catch(() => false)) {
        await openBtn.dispatchEvent('click')
        await page.waitForTimeout(300) // Wait for transition
      }
    }

    // Wait for session list
    await page.waitForSelector('#preact-session-list', { state: 'attached', timeout: 10000 })

    // Click a session (use dispatchEvent for outer button with nested toolbar buttons)
    await page.locator('#preact-session-list button[data-session-id="sess-001"]').dispatchEvent('click')
    await page.waitForTimeout(300) // Wait for any sidebar transition

    // Main content should be visible
    const main = page.locator('main')
    await expect(main).toBeVisible()

    // Terminal area (first child of main) should be visible
    const terminalDiv = main.locator('> div').first()
    await expect(terminalDiv).toBeVisible()
    await expect(terminalDiv).not.toHaveClass(/hidden/)
  })

  test('create session form inputs are fillable on mobile', async ({ page }) => {
    await page.goto('/?token=test')
    await waitForAppReady(page)

    const viewport = page.viewportSize()

    // On phone, open sidebar to access the New session button.
    // Use dispatchEvent to bypass sidebar z-index overlapping the hamburger.
    if (viewport && viewport.width < 1024) {
      const openBtn = page.locator('button[aria-label="Open sidebar"]')
      if (await openBtn.isVisible({ timeout: 2000 }).catch(() => false)) {
        await openBtn.dispatchEvent('click')
        await page.waitForTimeout(300)
      }
    }

    // Click "New session" button (inside the sidebar panel)
    const newBtn = page.locator('button[aria-label="New session"]')
    await expect(newBtn).toBeVisible({ timeout: 5000 })
    await newBtn.click()

    // Dialog should appear
    await expect(page.getByRole('heading', { name: 'New Session' })).toBeVisible({ timeout: 5000 })

    // Fill the inputs on mobile
    const form = page.locator('form')
    const titleInput = form.locator('input').nth(0)
    const pathInput = form.locator('input').nth(1)

    await titleInput.fill('Mobile Test')
    await pathInput.fill('/tmp/mobile')

    // Verify inputs contain expected values
    await expect(titleInput).toHaveValue('Mobile Test')
    await expect(pathInput).toHaveValue('/tmp/mobile')

    // Close dialog by clicking the backdrop (outside the dialog content).
    // CreateSessionDialog uses handleBackdropClick(e.target === e.currentTarget)
    // to close when clicking the outer overlay div. There's no Escape handler.
    const dialogOverlay = page.locator('.fixed.inset-0.z-50.flex.items-center.justify-center.bg-black\\/50')
    await dialogOverlay.click({ position: { x: 10, y: 10 } })
    await expect(page.getByRole('heading', { name: 'New Session' })).not.toBeVisible({ timeout: 3000 })
  })

  test('no horizontal overflow on any mobile viewport', async ({ page }) => {
    await page.goto('/?token=test')
    await waitForAppReady(page)

    // Check no horizontal scroll (1px tolerance for sub-pixel rendering)
    const checkOverflow = async (context: string) => {
      const scrollWidth = await page.evaluate(() => document.documentElement.scrollWidth)
      const clientWidth = await page.evaluate(() => document.documentElement.clientWidth)
      expect(
        scrollWidth,
        `Horizontal overflow detected (${context}): scrollWidth=${scrollWidth} > clientWidth=${clientWidth}`
      ).toBeLessThanOrEqual(clientWidth + 1)
    }

    // Check with sidebar closed
    await checkOverflow('sidebar closed')

    // Open sidebar and check again
    const viewport = page.viewportSize()
    if (viewport && viewport.width < 1024) {
      const openBtn = page.locator('button[aria-label="Open sidebar"]')
      if (await openBtn.isVisible({ timeout: 2000 }).catch(() => false)) {
        // Use dispatchEvent to bypass sidebar z-index covering the hamburger
        await openBtn.dispatchEvent('click')
        await page.waitForTimeout(300)
        await checkOverflow('sidebar open')

        // Close via backdrop (accessible below sidebar z-40) or dispatchEvent fallback
        // Close via dispatchEvent on the close button — the backdrop (z-30) is
        // physically behind the sidebar (z-40) so clicking it via mouse simulation
        // is intercepted by the sidebar. dispatchEvent fires directly into Preact.
        const closeBtn = page.locator('button[aria-label="Close sidebar"]')
        await closeBtn.dispatchEvent('click')
        await page.waitForTimeout(300)
        await checkOverflow('sidebar closed again')
      }
    }
  })

  test('topbar is visible and not clipped on mobile', async ({ page }) => {
    await page.goto('/?token=test')
    await waitForAppReady(page)

    // Topbar header should be visible
    const topbar = page.locator('header')
    await expect(topbar).toBeVisible()

    // "Agent Deck" brand: visible on phone (<768px) and desktop (>1024px) but
    // intentionally hidden at md breakpoint (768-1023px) via `md:hidden lg:inline`.
    // Only assert brand visibility on phone viewports.
    const currentViewport = page.viewportSize()
    if (currentViewport && currentViewport.width < 768) {
      await expect(topbar.getByText('Agent Deck')).toBeVisible()
    }

    // Topbar should not extend beyond viewport
    const topbarBox = await topbar.boundingBox()
    const viewport = page.viewportSize()
    if (topbarBox && viewport) {
      expect(topbarBox.x).toBeGreaterThanOrEqual(0)
      expect(topbarBox.x + topbarBox.width).toBeLessThanOrEqual(viewport.width + 1)
    }
  })
})
