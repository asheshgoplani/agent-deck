// e2e/keyboard-parity.spec.js -- Web/TUI keyboard parity (issue #780).
//
// One test per binding in the top-10 set. The bar is "key press produces
// observable state change in the DOM", not "key press chains through to a
// tmux side-effect" — that lives in the parity-actions matrix tests.
//
// Each test asserts a single binding's contract, then resets fixture state
// so the next test starts clean. The `?` overlay is exercised by the
// shortcuts-overlay test plus a visual-regression snapshot.

import { test, expect } from '@playwright/test'

async function waitForAppMount(page) {
  await page.waitForFunction(() => {
    const root = document.querySelector('#app, .app, [data-testid="app-root"], main')
    return root && root.textContent && root.textContent.trim().length > 50
  }, { timeout: 5000 })
  // Sidebar list takes one SSE roundtrip to populate after mount.
  await page.waitForSelector('.sess', { timeout: 5000 })
}

// Mobile (phone) uses a touch-first bottom-tab UX with a collapsed sidebar.
// The keyboard bindings still attach to `window`, but the sidebar `.sess`
// rows aren't visible, so DOM-based assertions like `/` focusing the filter
// or `j` selecting a `.sess.sel` aren't observable. We pin keyboard parity
// to viewports ≥ 768px (desktop + tablet) — that's where physical keyboards
// are the primary input mode anyway.
test.describe('keyboard parity (#780)', () => {
  test.skip(({ viewport }) => (viewport?.width || 1280) < 768, 'phone viewport: keyboard parity is desktop/tablet only')
  test.beforeEach(async ({ page, request }) => {
    await request.post('/__fixture/reset')
    await page.goto('/')
    await waitForAppMount(page)
  })

  test('/ focuses the sidebar filter input', async ({ page }) => {
    // Defensive: blur whatever may have stolen focus on mount.
    await page.evaluate(() => document.activeElement && document.activeElement.blur && document.activeElement.blur())
    await page.keyboard.press('/')
    const active = await page.evaluate(() => {
      const el = document.activeElement
      return el ? { tag: el.tagName, placeholder: el.placeholder || '' } : null
    })
    expect(active).not.toBeNull()
    expect(active.tag).toBe('INPUT')
    expect(active.placeholder.toLowerCase()).toContain('filter')
  })

  test('? opens the keyboard shortcuts overlay', async ({ page }) => {
    await page.keyboard.press('?')
    const overlay = page.locator('[data-testid="shortcuts-overlay"]')
    await expect(overlay).toBeVisible()
    // It must list the bindings; just check a couple of known labels.
    await expect(overlay).toContainText('Move focus down')
    // Shift+D was reworded "Stop" → "Close focused session" in #1129 (5b0dae2a:
    // non-destructive close, keeps metadata); the overlay label followed suit.
    await expect(overlay).toContainText('Close focused session')
    // Pressing ? again toggles it back off.
    await page.keyboard.press('?')
    await expect(overlay).toHaveCount(0)
  })

  test('j moves focus through group headers and sessions', async ({ page }) => {
    const titles = await page.locator('.sess .tt').allTextContents()
    test.skip(titles.length < 2, 'need at least two sessions for j to be observable')

    // Nothing selected initially. Rows are interleaved group headers and
    // sessions, so the first `j` lands on the first GROUP header.
    await page.keyboard.press('j')
    await page.waitForSelector('.side-group-head.sel', { timeout: 2000 })

    // The next `j` steps into that group's first session.
    await page.keyboard.press('j')
    await page.waitForSelector('.sess.sel', { timeout: 2000 })
    const first = await page.locator('.sess.sel .tt').textContent()
    expect(first).toBeTruthy()

    // And again to the second session in the same group.
    await page.keyboard.press('j')
    await page.waitForFunction((prev) => {
      const sel = document.querySelector('.sess.sel .tt')
      return sel && sel.textContent && sel.textContent !== prev
    }, first, { timeout: 2000 })
    expect(await page.locator('.sess.sel .tt').textContent()).not.toBe(first)
  })

  test('k moves focus back through the rendered rows', async ({ page }) => {
    const titles = await page.locator('.sess .tt').allTextContents()
    test.skip(titles.length < 2, 'need at least two sessions for k to be observable')

    // Bootstrap: j×3 → group header, first session, second session.
    await page.keyboard.press('j')
    await page.keyboard.press('j')
    await page.keyboard.press('j')
    await page.waitForSelector('.sess.sel', { timeout: 2000 })
    const before = await page.locator('.sess.sel .tt').textContent()

    await page.keyboard.press('k')
    await page.waitForFunction((prev) => {
      const sel = document.querySelector('.sess.sel .tt')
      return sel && sel.textContent && sel.textContent !== prev
    }, before, { timeout: 2000 })
    expect(await page.locator('.sess.sel .tt').textContent()).not.toBe(before)
  })

  test('Enter opens the focused session (terminal tab active)', async ({ page }) => {
    // Switch to a non-terminal tab first so we can observe the switch.
    await page.evaluate(() => {
      const e = new KeyboardEvent('keydown', { key: 'k', bubbles: true })
      document.dispatchEvent(e)
    })
    await page.keyboard.press('Enter')
    // Active terminal pane should be displayed (display: flex, not none).
    const visible = await page.evaluate(() => {
      // The TerminalPane wrapper has inline `display: flex` when active.
      const root = document.querySelector('.work-body')
      if (!root) return false
      const flex = Array.from(root.querySelectorAll('div')).find(d => d.style.display === 'flex')
      return !!flex
    })
    expect(visible).toBe(true)
  })

  test('Shift+Enter opens session in new browser tab', async ({ page, context }) => {
    // Focus a session explicitly first — nothing is focused by default now
    // that group headers are also selectable (j×2: group header, then the
    // first session).
    await page.keyboard.press('j')
    await page.keyboard.press('j')
    await page.waitForSelector('.sess.sel', { timeout: 2000 })

    const pagePromise = context.waitForEvent('page', { timeout: 2000 }).catch(() => null)
    await page.keyboard.down('Shift')
    await page.keyboard.press('Enter')
    await page.keyboard.up('Shift')
    const newPage = await pagePromise
    expect(newPage).not.toBeNull()
    expect(newPage.url()).toContain('#session=')
    await newPage.close()
  })

  test('n opens the New Session dialog', async ({ page }) => {
    await page.keyboard.press('n')
    // CreateSessionDialog renders as an overlay containing form fields.
    const dialog = page.locator('.overlay .dialog, [role="dialog"]').first()
    await expect(dialog).toBeVisible()
  })

  test('r surfaces the rename-not-supported toast (web API gap)', async ({ page }) => {
    // Focus a session explicitly first (j×2 — the first j lands on the
    // `work` group header) — the `r` fallback to an implicit first session
    // was removed once groups became selectable.
    await page.keyboard.press('j')
    await page.keyboard.press('j')
    await page.waitForSelector('.sess.sel', { timeout: 2000 })

    await page.keyboard.press('r')
    // Toast container shows the info-level message.
    const toast = page.locator('.toast', { hasText: /rename/i }).first()
    await expect(toast).toBeVisible({ timeout: 2000 })
  })

  test('Shift+D opens the stop-session confirm dialog', async ({ page }) => {
    // Focus a session explicitly first (j×2) — Shift+D no longer falls back
    // to an implicit first session.
    await page.keyboard.press('j')
    await page.keyboard.press('j')
    await page.waitForSelector('.sess.sel', { timeout: 2000 })

    await page.keyboard.down('Shift')
    await page.keyboard.press('D')
    await page.keyboard.up('Shift')
    // ConfirmDialog shows the close-session message. #1129 (5b0dae2a) reworked
    // Shift+D into a non-destructive "Close session" (kill process, keep
    // metadata), so the dialog copy is "Close session …" not "Stop session".
    const dialog = page.locator('.overlay .dialog, [role="dialog"]').first()
    await expect(dialog).toBeVisible()
    await expect(dialog).toContainText(/close session/i)
  })

  test('q closes an open modal', async ({ page }) => {
    // Open the shortcuts overlay, then dismiss with `q`.
    await page.keyboard.press('?')
    await expect(page.locator('[data-testid="shortcuts-overlay"]')).toBeVisible()
    await page.keyboard.press('q')
    await expect(page.locator('[data-testid="shortcuts-overlay"]')).toHaveCount(0)
  })

  test('Esc unfocuses the search input and closes modals', async ({ page }) => {
    await page.keyboard.press('/')
    // confirm input is focused
    let activeTag = await page.evaluate(() => document.activeElement?.tagName)
    expect(activeTag).toBe('INPUT')
    await page.keyboard.press('Escape')
    activeTag = await page.evaluate(() => document.activeElement?.tagName)
    expect(activeTag).not.toBe('INPUT')
  })

  test('typing in the filter input does NOT trigger navigation bindings', async ({ page }) => {
    // This is the critical regression guard: pressing `j` or `n` inside the
    // search field must type the letter, not trigger navigation.
    await page.keyboard.press('/')
    await page.keyboard.type('jn')
    const value = await page.evaluate(() =>
      document.querySelector('.side-filter input')?.value || '',
    )
    expect(value).toBe('jn')
    // And no New Session dialog should have opened.
    const dialog = page.locator('.dialog', { hasText: /New session/i })
    await expect(dialog).toHaveCount(0)
  })
})

test.describe('keyboard parity: visual', () => {
  test.skip(({ viewport }) => (viewport?.width || 1280) < 768, 'phone viewport: keyboard parity is desktop/tablet only')
  test.beforeEach(async ({ page, request }) => {
    await request.post('/__fixture/reset')
    await page.goto('/')
    await waitForAppMount(page)
  })

  test('shortcuts overlay renders consistently', async ({ page }) => {
    await page.keyboard.press('?')
    const overlay = page.locator('[data-testid="shortcuts-overlay"]')
    await expect(overlay).toBeVisible()
    await page.waitForTimeout(200)
    await expect(overlay).toHaveScreenshot('shortcuts-overlay.png')
  })
})
