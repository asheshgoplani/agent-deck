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

  test('j moves focus to the next session', async ({ page }) => {
    const titles = await page.locator('.sess .tt').allTextContents()
    test.skip(titles.length < 2, 'need at least two sessions for j to be observable')
    // No session selected initially; first `j` selects the first session.
    await page.keyboard.press('j')
    await page.waitForSelector('.sess.sel', { timeout: 2000 })
    const first = await page.locator('.sess.sel .tt').textContent()
    expect(first).toBeTruthy()
    // Second `j` should move to a different session.
    await page.keyboard.press('j')
    await page.waitForFunction((prev) => {
      const sel = document.querySelector('.sess.sel .tt')
      return sel && sel.textContent && sel.textContent !== prev
    }, first, { timeout: 2000 })
    const second = await page.locator('.sess.sel .tt').textContent()
    expect(second).not.toBe(first)
  })

  test('k moves focus to the previous session', async ({ page }) => {
    const titles = await page.locator('.sess .tt').allTextContents()
    test.skip(titles.length < 2, 'need at least two sessions for k to be observable')
    // Bootstrap: select first, then advance once with j so k has somewhere to go.
    await page.keyboard.press('j')
    await page.waitForSelector('.sess.sel', { timeout: 2000 })
    await page.keyboard.press('j')
    await page.waitForTimeout(100)
    const before = await page.locator('.sess.sel .tt').textContent()
    await page.keyboard.press('k')
    await page.waitForFunction((prev) => {
      const sel = document.querySelector('.sess.sel .tt')
      return sel && sel.textContent && sel.textContent !== prev
    }, before, { timeout: 2000 })
    const after = await page.locator('.sess.sel .tt').textContent()
    expect(after).not.toBe(before)
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
    await page.keyboard.press('r')
    // Toast container shows the info-level message.
    const toast = page.locator('.toast', { hasText: /rename/i }).first()
    await expect(toast).toBeVisible({ timeout: 2000 })
  })

  test('Shift+D opens the stop-session confirm dialog', async ({ page }) => {
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
