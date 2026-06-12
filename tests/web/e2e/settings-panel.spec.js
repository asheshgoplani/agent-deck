// e2e/settings-panel.spec.js -- Settings drawer (SettingsPanel.js) coverage.
//
// Entry point (verified against source): the ONLY way to open the settings
// drawer is the command palette's "Settings drawer" command
// (CommandPalette.js cmd-settings → infoDrawerOpenSignal). The Topbar gear
// icon opens the TWEAKS panel (tweaksOpenSignal), NOT settings — easy to
// confuse, so the first test pins the palette path explicitly.
//
// The drawer renders in AppShell.js as .overlay > .dialog with kicker
// "SETTINGS" and mounts SettingsPanel, which reads GET /api/settings via
// settingsSignal and renders .kv rows (profile / version / read-only /
// web mutations / hidden tools / picker tools).
//
// Close paths (verified): the × button (aria-label="Close settings"),
// backdrop click (overlay onClick), Esc and q (AppShell closeAllModals
// resets infoDrawerOpenSignal).
//
// Viewports: opening requires Ctrl+K (the .top-search button is CSS-hidden
// ≤1000px, so there is no pointer entry on tablet/phone). Following the
// keyboard-parity convention we run on desktop + tablet and skip phone,
// where the touch-first layout exposes no settings entry point at all.

import { test, expect } from '@playwright/test'

async function waitForAppMount(page) {
  await page.waitForFunction(() => {
    const root = document.querySelector('#app, .app, [data-testid="app-root"], main')
    return root && root.textContent && root.textContent.trim().length > 50
  }, { timeout: 5000 })
  // Sidebar list takes one SSE roundtrip to populate after mount.
  await page.waitForSelector('.sess', { timeout: 5000 })
}

async function openSettingsDrawer(page) {
  await page.keyboard.press('Control+k')
  await expect(page.locator('[data-testid="command-palette"]')).toBeVisible()
  await page.locator('[data-testid="palette-cmd-row"]', { hasText: 'Settings drawer' }).click()
  await expect(page.locator('[data-testid="settings-panel"]')).toBeVisible({ timeout: 5000 })
}

test.describe('settings panel', () => {
  test.skip(
    ({ viewport }) => (viewport?.width || 1280) < 768,
    'phone viewport: no settings entry point (palette is keyboard-only; .top-search hidden ≤1000px)',
  )

  test.beforeEach(async ({ page, request }) => {
    await request.post('/__fixture/reset')
    await page.goto('/')
    await waitForAppMount(page)
  })

  test('palette "Settings drawer" command opens the settings dialog', async ({ page }) => {
    await openSettingsDrawer(page)
    // AppShell renders the drawer as a dialog with the SETTINGS kicker; the
    // palette itself closes when the command runs.
    const dialog = page.locator('.overlay .dialog', { has: page.locator('[data-testid="settings-panel"]') })
    await expect(dialog).toBeVisible()
    await expect(dialog.locator('.kicker')).toHaveText('SETTINGS')
    await expect(page.locator('[data-testid="command-palette"]')).toHaveCount(0)
  })

  test('panel rows mirror GET /api/settings (profile, version, mutations, tools)', async ({ page, request }) => {
    // Ground truth straight from the API the panel itself consumes; the
    // fixture serves profile "fixture" with mutations enabled (see
    // parity-actions.spec.js settings test for the contract shape).
    const res = await request.get('/api/settings')
    expect(res.ok()).toBe(true)
    const body = await res.json()
    expect(body.profile).toBe('fixture')
    expect(body.webMutations).toBe(true)

    await openSettingsDrawer(page)
    await expect(page.locator('[data-testid="settings-profile"] .v')).toHaveText('fixture')
    await expect(page.locator('[data-testid="settings-version"] .v')).toHaveText(body.version)
    await expect(page.locator('[data-testid="settings-read-only"] .v')).toHaveText(body.readOnly ? 'yes' : 'no')
    await expect(page.locator('[data-testid="settings-web-mutations"] .v')).toHaveText('enabled')
    // SettingsPanel joins the arrays with ', ' and falls back to 'none'.
    const hiddenExpected = (body.hiddenTools || []).join(', ') || 'none'
    await expect(page.locator('[data-testid="settings-hidden-tools"] .v')).toHaveText(hiddenExpected)
    const pickerText = await page.locator('[data-testid="settings-picker-tools"] .v').textContent()
    expect(pickerText).toContain('shell')
    for (const tool of body.pickerTools || []) {
      expect(pickerText).toContain(tool)
    }
  })

  test('× button closes the drawer', async ({ page }) => {
    await openSettingsDrawer(page)
    await page.locator('button[aria-label="Close settings"]').click()
    await expect(page.locator('[data-testid="settings-panel"]')).toHaveCount(0)
  })

  test('Esc closes the drawer', async ({ page }) => {
    await openSettingsDrawer(page)
    await page.keyboard.press('Escape')
    await expect(page.locator('[data-testid="settings-panel"]')).toHaveCount(0)
  })

  test('q closes the drawer (TUI parity dismiss)', async ({ page }) => {
    await openSettingsDrawer(page)
    await page.keyboard.press('q')
    await expect(page.locator('[data-testid="settings-panel"]')).toHaveCount(0)
  })

  test('backdrop click closes the drawer, clicking inside does not', async ({ page }) => {
    await openSettingsDrawer(page)
    // Clicks inside the dialog are stopPropagation'd in AppShell.
    await page.locator('[data-testid="settings-profile"]').click()
    await expect(page.locator('[data-testid="settings-panel"]')).toBeVisible()
    // Clicking the overlay backdrop (top-left corner, outside the dialog)
    // triggers the overlay onClick → drawer closes.
    await page.locator('.overlay', { has: page.locator('[data-testid="settings-panel"]') })
      .click({ position: { x: 5, y: 5 } })
    await expect(page.locator('[data-testid="settings-panel"]')).toHaveCount(0)
  })
})
