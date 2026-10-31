// e2e/mobile-touch.spec.js -- phone touch contract for an open session (#2680).
//
// Runs on the phone projects only: chromium-phone (CI, with touch emulation
// turned on here) and webkit-iphone (opt-in with AGENT_DECK_WEBKIT=1, see
// playwright.config.js), which is the engine iPhone users actually hit.
//
// What it pins:
//   - A stationary one-finger tap on the terminal focuses xterm's helper
//     textarea, which is what makes iOS show the keyboard. Before #2680 the
//     only focus path on touch was xterm's own mousedown handler, and the
//     mousedown never came: installTouchScroll called preventDefault() on
//     every touchmove, and a real finger tap nearly always jitters a pixel or
//     two, so iOS dropped the compatibility mouse events for the tap. The
//     synthetic taps below carry only touch events (no compat mouse events),
//     which is exactly what xterm saw on the phone.
//   - A scroll drag and a two-finger gesture do not focus (no keyboard pop
//     while scrolling or pinching).
//   - With a session open there is a visible back-to-list control in the
//     work head, and tapping it returns to the Fleet session list.

import { test, expect } from '@playwright/test'

test.describe('phone touch: open session (#2680)', () => {
  test.skip(
    ({ viewport }) => (viewport?.width || 1280) >= 768,
    'phone-only: desktop/tablet keep mouse focus and the sidebar',
  )
  // chromium-phone is a plain Desktop Chrome descriptor; give it touch so
  // TouchEvent exists and page.touchscreen works. webkit-iphone already has it.
  test.use({ hasTouch: true })

  test.beforeEach(async ({ page, request }) => {
    await request.post('/__fixture/reset')
    await page.goto('/')
    const tile = page.locator('[data-testid="fleet-session-tile"][data-session-id="sess-001"]')
    await expect(tile).toBeVisible({ timeout: 5000 })
    await tile.tap()
    await expect(page.locator('[data-testid="mobile-tab-terminal"]')).toHaveClass(/\bon\b/)
    await expect(page.locator('.term-frame')).toBeVisible()
    await expect(page.locator('.xterm-helper-textarea')).toBeAttached()
    // Start every case unfocused.
    await page.evaluate(() => document.activeElement && document.activeElement.blur())
  })

  // Dispatch a touch gesture made only of touch events on the xterm screen.
  // points: [{x, y}] per step for finger 1; the first is touchstart, the last
  // is where the finger lifts. extraFinger adds a second finger for the whole
  // gesture.
  async function touchGesture(page, { moves = [], extraFinger = false } = {}) {
    await page.evaluate(({ moves, extraFinger }) => {
      const target = document.querySelector('.xterm-screen') || document.querySelector('.xterm')
      const r = target.getBoundingClientRect()
      const cx = r.left + r.width / 2
      const cy = r.top + r.height / 2
      // WebKit has no Touch constructor, so build touch-shaped records and a
      // plain cancelable Event carrying them -- the same fields the page's
      // touch handlers (and xterm's) read, in every engine.
      const mk = (id, dx, dy) => ({
        identifier: id, target, clientX: cx + dx, clientY: cy + dy,
        pageX: cx + dx, pageY: cy + dy, screenX: cx + dx, screenY: cy + dy,
      })
      const list = (arr) => Object.assign([...arr], { item: (i) => arr[i] || null })
      const fire = (type, touches, changed) => {
        const ev = new Event(type, { bubbles: true, cancelable: true })
        Object.defineProperties(ev, {
          touches: { value: list(touches) },
          targetTouches: { value: list(touches) },
          changedTouches: { value: list(changed) },
        })
        target.dispatchEvent(ev)
      }
      const f2 = extraFinger ? [mk(2, 40, 40)] : []
      let cur = mk(1, 0, 0)
      fire('touchstart', [cur, ...f2], [cur, ...f2])
      for (const [dx, dy] of moves) {
        cur = mk(1, dx, dy)
        fire('touchmove', [cur, ...f2], [cur])
      }
      if (extraFinger) fire('touchend', [cur], f2)
      fire('touchend', [], [cur])
    }, { moves, extraFinger })
  }

  const activeIsXterm = (page) =>
    page.evaluate(() => !!document.activeElement && document.activeElement.classList.contains('xterm-helper-textarea'))

  test('a stationary tap focuses the terminal (keyboard target)', async ({ page }) => {
    await touchGesture(page)
    await expect.poll(() => activeIsXterm(page)).toBe(true)
  })

  test('a tap with finger jitter still focuses the terminal', async ({ page }) => {
    // A real fingertip moves a pixel or two between touchstart and touchend.
    await touchGesture(page, { moves: [[1, 2], [2, 1]] })
    await expect.poll(() => activeIsXterm(page)).toBe(true)
  })

  test('a real touchscreen tap focuses the terminal', async ({ page }) => {
    // Tap the middle of the terminal area (xterm's mount node), where a
    // thumb lands. Not .xterm-screen: in headless WebKit builds without a GPU
    // xterm can lay out at 0x0, which would put the tap on the frame edge.
    const box = await page.evaluate(() => {
      const r = document.querySelector('.xterm').parentElement.getBoundingClientRect()
      return { x: r.left, y: r.top, width: r.width, height: r.height }
    })
    await page.touchscreen.tap(box.x + box.width / 2, box.y + box.height / 2)
    await expect.poll(() => activeIsXterm(page)).toBe(true)
  })

  test('a scroll drag does not focus the terminal', async ({ page }) => {
    await touchGesture(page, { moves: [[0, -20], [0, -60], [0, -120]] })
    await page.waitForTimeout(150)
    expect(await activeIsXterm(page)).toBe(false)
  })

  test('a two-finger gesture does not focus the terminal', async ({ page }) => {
    await touchGesture(page, { extraFinger: true })
    await page.waitForTimeout(150)
    expect(await activeIsXterm(page)).toBe(false)
  })

  test('a visible back-to-list control returns to the session list', async ({ page }) => {
    const back = page.locator('[data-testid="mobile-back-to-list"]')
    await expect(back).toBeVisible()
    await back.tap()
    await expect(page.locator('[data-testid="mobile-tab-fleet"]')).toHaveClass(/\bon\b/)
    await expect(page.locator('[data-testid="fleet-pane"]')).toBeVisible()
    await expect(page.locator('[data-testid="fleet-session-tile"]').first()).toBeVisible()
    // On the list itself the control is gone.
    await expect(back).toBeHidden()
  })
})
