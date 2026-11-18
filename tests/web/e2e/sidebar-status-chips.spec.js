// e2e/sidebar-status-chips.spec.js -- every sidebar status filter chip sits
// inside the sidebar and can be clicked.
//
// Before: the filter input plus five chips did not fit the 300px rail; the
// "stopped" and "error" chips were drawn past the sidebar's right edge, over
// the main pane, and a click on them timed out.
//
// The sidebar is desktop/tablet only (hidden on phones), so phone skips.
import { test, expect } from '@playwright/test'

const CHIPS = ['running', 'waiting', 'idle', 'stopped', 'error']

for (const width of [1280, 1440, 1920]) {
  test(`at ${width}px all five status chips are inside the sidebar and toggle on click`, async ({ page, request, viewport }) => {
    test.skip(!!viewport && viewport.width < 768, 'sidebar is desktop/tablet-only')
    await request.post('/__fixture/reset')
    await page.setViewportSize({ width, height: 900 })
    await page.goto('/')
    await page.waitForSelector('.sess', { state: 'attached' })
    await expect(page.getByTestId('status-chip-error')).toBeVisible()

    const side = await page.locator('.sidebar').boundingBox()
    for (const id of CHIPS) {
      const chip = page.getByTestId(`status-chip-${id}`)
      const box = await chip.boundingBox()
      expect(box.x, `${id} chip left edge`).toBeGreaterThanOrEqual(side.x)
      expect(box.x + box.width, `${id} chip right edge`).toBeLessThanOrEqual(side.x + side.width)
      await chip.click({ timeout: 2000 })
      await expect(chip).toHaveClass(/\bon\b/)
      await chip.click({ timeout: 2000 })
      await expect(chip).not.toHaveClass(/\bon\b/)
    }
  })
}
