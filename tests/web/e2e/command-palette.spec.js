// e2e/command-palette.spec.js -- Cmd+K / Ctrl+K command palette coverage.
//
// Exercises CommandPalette.js end-to-end against the seeded fixture:
//   - Ctrl/Cmd+K opens the palette, including while focus is inside an input
//     (AppShell.js checks the chord BEFORE the in-field guard).
//   - COMMANDS section lists the 8 commands (New session is present because
//     the fixture runs with mutations enabled) and SESSIONS lists the 4
//     seeded sessions (agent-deck, frontend, innotrade-api, scratch).
//   - Typing filters rows by label (case-insensitive substring), with a
//     "No matches." empty state.
//   - Clicking a session row selects it + switches to the terminal tab;
//     clicking a command row (Costs dashboard) switches tabs.
//   - Esc closes the palette.
//
// Deliberately NOT covered: arrow-key navigation / Enter-to-run inside the
// palette — CommandPalette.js only handles Escape on the input; there is no
// ArrowUp/ArrowDown/Enter selection logic to test.

import { test, expect } from '@playwright/test'

async function waitForAppMount(page) {
  await page.waitForFunction(() => {
    const root = document.querySelector('#app, .app, [data-testid="app-root"], main')
    return root && root.textContent && root.textContent.trim().length > 50
  }, { timeout: 5000 })
  // Sidebar list takes one SSE roundtrip to populate after mount.
  await page.waitForSelector('.sess', { timeout: 5000 })
}

async function openPalette(page) {
  await page.keyboard.press('Control+k')
  await expect(page.locator('[data-testid="command-palette"]')).toBeVisible()
}

// The palette itself renders on phones too, but our assertions lean on the
// sidebar (`.sess` rows for mount/selection checks) and the top tab strip,
// both of which are display:none below 720px. Keep this desktop/tablet.
test.describe('command palette (Ctrl/Cmd+K)', () => {
  test.skip(({ viewport }) => (viewport?.width || 1280) < 768, 'phone viewport: palette assertions rely on sidebar + top tabs (desktop/tablet only)')

  test.beforeEach(async ({ page, request }) => {
    await request.post('/__fixture/reset')
    await page.goto('/')
    await waitForAppMount(page)
  })

  test('Ctrl+K opens the palette and focuses its input', async ({ page }) => {
    await openPalette(page)
    // CommandPalette focuses the input on open (setTimeout 0).
    await expect(page.locator('[data-testid="palette-input"]')).toBeFocused()
  })

  test('Ctrl+K opens the palette even while typing in an input', async ({ page }) => {
    // Focus the sidebar filter first — the Ctrl/Cmd+K branch in AppShell's
    // keydown handler runs before the `inField` early-return, so the chord
    // must work from inside a text field.
    await page.keyboard.press('/')
    const activeTag = await page.evaluate(() => document.activeElement?.tagName)
    expect(activeTag).toBe('INPUT')
    await openPalette(page)
  })

  test('lists COMMANDS and SESSIONS sections with seeded content', async ({ page }) => {
    await openPalette(page)
    const palette = page.locator('[data-testid="command-palette"]')
    await expect(palette.locator('.sec')).toHaveText(['COMMANDS', 'SESSIONS'])

    // 9 commands: New session (mutations enabled in the fixture) + the 8
    // static entries, in source order from CommandPalette.js.
    const cmdRows = palette.locator('[data-testid="palette-cmd-row"]')
    await expect(cmdRows).toHaveCount(9)
    await expect(cmdRows).toContainText([
      'New session',
      'Open Fleet',
      'Open Terminal',
      'Costs dashboard',
      'Session search',
      'Archived sessions',
      'Open Tweaks',
      'Keyboard shortcuts',
      'Settings drawer',
    ])

    // 4 seeded sessions in fixture order (seed() in web-fixture/main.go).
    const sessRows = palette.locator('[data-testid="palette-session-row"]')
    await expect(sessRows).toHaveCount(4)
    await expect(sessRows).toContainText(['agent-deck', 'frontend', 'innotrade-api', 'scratch'])
  })

  test('typing filters rows by title', async ({ page }) => {
    await openPalette(page)
    const palette = page.locator('[data-testid="command-palette"]')
    await palette.locator('[data-testid="palette-input"]').fill('frontend')

    // Only the matching session remains; non-matching sessions and all
    // commands (none contain "frontend") disappear.
    const sessRows = palette.locator('[data-testid="palette-session-row"]')
    await expect(sessRows).toHaveCount(1)
    await expect(sessRows.first()).toContainText('frontend')
    await expect(palette.locator('[data-testid="palette-cmd-row"]')).toHaveCount(0)
    await expect(palette.locator('.row', { hasText: 'agent-deck' })).toHaveCount(0)
  })

  test('a query with no matches shows the empty state', async ({ page }) => {
    await openPalette(page)
    const palette = page.locator('[data-testid="command-palette"]')
    await palette.locator('[data-testid="palette-input"]').fill('zzz-no-such-thing')
    await expect(palette.locator('.row')).toHaveCount(0)
    await expect(palette.locator('[data-testid="palette-empty"]')).toBeVisible()
    await expect(palette.locator('[data-testid="palette-empty"]')).toContainText('No matches')
  })

  test('selecting a session row selects it and switches to the terminal tab', async ({ page }) => {
    await openPalette(page)
    await page.locator('[data-testid="palette-session-row"]', { hasText: 'frontend' }).click()

    // Palette closes...
    await expect(page.locator('[data-testid="command-palette"]')).toHaveCount(0)
    // ...the session is selected in the sidebar...
    await expect(page.locator('.sess.sel .tt')).toHaveText('frontend')
    // ...and the terminal tab is now active.
    await expect(page.locator('.top-tab.active')).toHaveText('Terminal')
  })

  test('selecting a command (Costs dashboard) switches the tab', async ({ page }) => {
    await openPalette(page)
    await page.locator('[data-testid="palette-cmd-row"]', { hasText: 'Costs dashboard' }).click()

    await expect(page.locator('[data-testid="command-palette"]')).toHaveCount(0)
    await expect(page.locator('.top-tab.active')).toContainText('Costs')
    await expect(page.locator('.costs')).toBeVisible()
  })

  test('Esc closes the palette', async ({ page }) => {
    await openPalette(page)
    await page.keyboard.press('Escape')
    await expect(page.locator('[data-testid="command-palette"]')).toHaveCount(0)
  })
})
