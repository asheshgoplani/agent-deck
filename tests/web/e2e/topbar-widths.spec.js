// e2e/topbar-widths.spec.js -- the top bar stays usable between tablet and
// wide desktop widths.
//
// Before: at 1280 and 1440px the search box shrank to ~22px and its ⌘K label
// was drawn on top of the "Command Center" tab; at 768px the tab strip ran to
// x≈1000, past the window, with no way to reach the clipped tabs.
//
// Phones (<=720px) hide the top tabs and use the bottom tab bar instead.
import { test, expect } from '@playwright/test'

test.use({ serviceWorkers: 'block' })

for (const width of [768, 1024, 1280, 1440, 1920]) {
  test(`at ${width}px the search box does not overlap the tabs and every tab is reachable`, async ({ page, request, viewport }) => {
    test.skip(!!viewport && viewport.width < 721, 'phone layout has no top tabs')
    await request.post('/__fixture/reset')
    await page.setViewportSize({ width, height: 900 })
    await page.goto('/')
    await page.waitForSelector('.sess')

    const geo = await page.evaluate(() => {
      const r = el => { const b = el.getBoundingClientRect(); return { left: b.left, right: b.right, width: b.width } }
      const visible = el => el && getComputedStyle(el).display !== 'none' && el.getBoundingClientRect().width > 0
      const search = document.querySelector('.top-search')
      const kbd = document.querySelector('.top-search .kbd')
      const strip = document.querySelector('.top-tabs')
      const tabs = [...strip.querySelectorAll('.top-tab')]
      return {
        search: visible(search) ? r(search) : null,
        kbd: visible(kbd) ? r(kbd) : null,
        strip: r(strip),
        stripScrollable: ['auto', 'scroll'].includes(getComputedStyle(strip).overflowX),
        stripOverflows: strip.scrollWidth > strip.clientWidth + 1,
        tabs: tabs.map(t => ({ label: t.textContent.trim(), ...r(t) })),
        vw: window.innerWidth,
      }
    })

    if (geo.search) {
      expect(geo.search.right, 'search box ends before the tab strip').toBeLessThanOrEqual(geo.strip.left + 1)
      if (geo.kbd) expect(geo.kbd.right, '⌘K label stays inside the search box').toBeLessThanOrEqual(geo.search.right + 1)
    }
    // The strip itself never runs past the window.
    expect(geo.strip.right).toBeLessThanOrEqual(geo.vw + 1)
    // Tabs that do not fit must be reachable by scrolling the strip, not clipped.
    if (geo.stripOverflows) expect(geo.stripScrollable, 'overflowing tab strip scrolls').toBe(true)

    // Every tab can be clicked and becomes the active one.
    const tabs = page.locator('.top-tabs .top-tab')
    const n = await tabs.count()
    for (let i = 0; i < n; i++) {
      await tabs.nth(i).click({ timeout: 2000 })
      await expect(tabs.nth(i)).toHaveClass(/\bactive\b/)
    }
  })
}
