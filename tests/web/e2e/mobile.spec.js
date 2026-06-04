// e2e/mobile.spec.js -- mobile shell structure & responsiveness guards.
// NOTE: chromium phone profile cannot reproduce iOS-Safari vh/toolbar
// behavior; these tests guard CSS structure/regressions only. The Safari
// bottom-nav fix is verified manually on a real device.
import { test, expect } from '@playwright/test'

async function waitForAppMount(page) {
  await page.waitForFunction(() => {
    const root = document.querySelector('#app-root-grid, .app')
    return root && root.textContent && root.textContent.trim().length > 50
  }, { timeout: 5000 })
}

test.describe('mobile shell', () => {
  test.beforeEach(async ({ request }) => { await request.post('/__fixture/reset') })

  test('viewport meta opts into safe areas', async ({ page }) => {
    await page.goto('/')
    const content = await page.locator('meta[name="viewport"]').getAttribute('content')
    expect(content).toContain('viewport-fit=cover')
  })

  test('bottom nav renders inside the viewport on phone', async ({ page, viewport }) => {
    test.skip((viewport?.width || 1280) > 720, 'phone-only: mob-tabs hidden ≥721px')
    await page.goto('/')
    await waitForAppMount(page)
    const tabs = page.locator('.mob-tabs')
    await expect(tabs).toBeVisible()
    const box = await tabs.boundingBox()
    const vp = page.viewportSize()
    expect(box).not.toBeNull()
    // bottom edge sits at/above the viewport floor (allow 1px rounding)
    expect(box.y + box.height).toBeLessThanOrEqual(vp.height + 1)
    expect(box.width).toBeGreaterThan(vp.width * 0.9) // full-width bar
  })

  test('bottom nav is hidden on desktop', async ({ page, viewport }) => {
    test.skip((viewport?.width || 1280) <= 720, 'desktop/tablet-only')
    await page.goto('/')
    await waitForAppMount(page)
    await expect(page.locator('.mob-tabs')).toBeHidden()
  })

  test('keyboard-inset variable initializes', async ({ page }) => {
    await page.goto('/')
    await waitForAppMount(page)
    // Read the INLINE style the visualViewport effect writes (not the
    // computed value — styles.css :root declares --keyboard-inset:0px, so
    // the computed value is "0px" even if the effect never ran). Poll
    // because the effect commits just after mount.
    await expect.poll(() => page.evaluate(() =>
      document.documentElement.style.getPropertyValue('--keyboard-inset').trim())).toMatch(/px$/)
  })

  test('hamburger opens the session drawer and selecting closes it', async ({ page, viewport }) => {
    test.skip((viewport?.width || 1280) > 720, 'phone-only: drawer + hamburger are mobile-only')
    await page.goto('/')
    await waitForAppMount(page)
    await page.locator('.top-burger').click()
    const sidebar = page.locator('.sidebar')
    await expect(sidebar).toBeVisible()
    // a seeded session row exists
    const firstSess = page.locator('.sidebar .sess').first()
    await expect(firstSess).toBeVisible()
    // Click the row's left edge (sigil/title area), NOT the center: hovering
    // the row reveals the right-aligned .actions strip, and with the enlarged
    // 34px mobile touch targets it covers the row center, so a center click
    // lands on an action button (which stops propagation) instead of the row's
    // onSelect. A real touch device has no hover; this mirrors tapping the title.
    await firstSess.click({ position: { x: 12, y: 10 } })
    // drawer closes (body class removed)
    await expect.poll(() => page.evaluate(() =>
      document.body.classList.contains('drawer-open'))).toBe(false)
  })

  test('drawer nav can reach the Costs destination', async ({ page, viewport }) => {
    test.skip((viewport?.width || 1280) > 720, 'phone-only: drawer nav is mobile-only')
    await page.goto('/')
    await waitForAppMount(page)
    await page.locator('.top-burger').click()
    await page.locator('.side-nav-btn', { hasText: 'Costs' }).click()
    await expect.poll(() => page.evaluate(() =>
      JSON.parse(localStorage.getItem('agentdeck.tab')))).toBe('costs')
    await expect.poll(() => page.evaluate(() =>
      document.body.classList.contains('drawer-open'))).toBe(false)
  })

  test('per-group + button pre-fills the group in the new-session dialog', async ({ page }) => {
    await page.goto('/')
    await waitForAppMount(page)
    // Fleet pane is the default tab; click the first group card "+".
    const plus = page.locator('.gc-new').first()
    await expect(plus).toBeVisible()
    await plus.click()
    const groupSelect = page.locator('.dialog .field', { hasText: 'GROUP' }).locator('select')
    await expect(groupSelect).toBeVisible()
    expect(await groupSelect.inputValue()).not.toBe('') // a group is pre-selected
  })

  test('no horizontal overflow on key panes at phone width', async ({ page, viewport }) => {
    test.skip((viewport?.width || 1280) > 720, 'phone-only: overflow check is for phone width')
    await page.goto('/')
    await waitForAppMount(page)
    for (const tab of ['fleet', 'costs', 'search']) {
      await page.evaluate(t => { localStorage.setItem('agentdeck.tab', JSON.stringify(t)) }, tab)
      await page.reload()
      await waitForAppMount(page)
      const overflow = await page.evaluate(() =>
        document.documentElement.scrollWidth - document.documentElement.clientWidth)
      expect(overflow, `tab=${tab}`).toBeLessThanOrEqual(1)
    }
  })

  test('session details disclosure is available on phone', async ({ page, viewport }) => {
    test.skip((viewport?.width || 1280) > 720, 'phone-only: mobile-detail is visible on phone')
    await page.goto('/')
    await waitForAppMount(page)
    await page.evaluate(() => localStorage.setItem('agentdeck.tab', JSON.stringify('terminal')))
    await page.reload(); await waitForAppMount(page)
    await expect(page.locator('.mobile-detail > summary')).toBeVisible()
    // Open the disclosure and confirm the rail surfaces — exercises the
    // `.mobile-detail-body .rightrail { display:flex !important }` override
    // that defeats the mobile `.rightrail { display:none }` hide rule. The
    // locator is scoped to .mobile-detail-body (there are two .rightrail in
    // the DOM: the grid rail + this disclosure copy).
    await page.locator('.mobile-detail > summary').click()
    await expect(page.locator('.mobile-detail-body .rightrail')).toBeVisible()
  })

  test('session details disclosure hidden on desktop', async ({ page, viewport }) => {
    test.skip((viewport?.width || 1280) <= 720, 'desktop/tablet-only: mobile-detail is hidden ≥721px')
    await page.goto('/')
    await waitForAppMount(page)
    await page.evaluate(() => localStorage.setItem('agentdeck.tab', JSON.stringify('terminal')))
    await page.reload(); await waitForAppMount(page)
    // toHaveCount(0): the disclosure must be ABSENT from the desktop DOM, not
    // merely hidden. Mounting it on desktop adds a 2nd RightRail and breaks
    // unscoped RightRail queries (e.g. children-panel's `.card`). toBeHidden()
    // passes for an absent element and would miss that regression.
    await expect(page.locator('.mobile-detail')).toHaveCount(0)
  })
})
