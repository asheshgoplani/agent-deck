// e2e/tweaks-rail.spec.js — Tweaks panel + right rail configuration coverage.
//
// Exercises the appearance/layout surface that no other spec touches:
//   - TweaksPanel (Topbar gear button): accent swatches, density seg buttons,
//     right-rail visibility switch, close via ×/q/Esc.
//   - RightRail: per-card panel toggles (rail-add picker) and the Overview
//     card's seeded session fields.
//   - Topbar `]` icon button as the pointer-driven rail toggle (the `]` KEY
//     binding lives in keyboard-extras.spec.js).
//
// Persistence contract (uiState.js): accent/density/rail/rightRailPanels are
// JSON-persisted to localStorage under agentdeck.accent / agentdeck.density /
// agentdeck.rail / agentdeck.rightRailPanels and mirrored to dataset
// attributes on <html> and <body> by the uiState effect.
//
// Viewport notes (app.css @media max-width 720px): the phone layout keeps the
// .top-right cluster (Tweaks button) and restyles .tweaks for mobile, so the
// tweaks panel + accent/density tests run on ALL projects. The right rail is
// `display: none` on phone regardless of data-rail, so every rail-visibility
// assertion is pinned to viewports ≥ 768px like keyboard-parity.spec.js.

import { test, expect } from '@playwright/test'

// Phone-safe mount wait: the sidebar `.sess` rows are display:none on phone,
// so wait on the always-visible Topbar Tweaks button instead.
async function waitForTopbar(page) {
  await page.waitForSelector('button[aria-label="Tweaks"]', { timeout: 5000 })
}

// Desktop/tablet mount wait — also waits for the SSE-fed session list so the
// right rail has data to render (same shape as keyboard-parity.spec.js).
async function waitForAppMount(page) {
  await waitForTopbar(page)
  await page.waitForSelector('.sess', { timeout: 5000 })
}

async function openTweaks(page) {
  await page.locator('button[aria-label="Tweaks"]').click()
  await expect(page.locator('[data-testid="tweaks-panel"]')).toBeVisible()
}

const lsValue = (page, key) => page.evaluate(k => localStorage.getItem(k), key)

test.describe('tweaks panel (all viewports)', () => {
  test.beforeEach(async ({ page, request }) => {
    await request.post('/__fixture/reset')
    await page.goto('/')
    await waitForTopbar(page)
  })

  test('topbar gear opens the panel; × closes it', async ({ page }) => {
    await openTweaks(page)
    const panel = page.locator('[data-testid="tweaks-panel"]')
    await expect(panel).toContainText('ACCENT')
    await expect(panel).toContainText('DENSITY')
    await expect(panel).toContainText('RIGHT RAIL')
    await panel.locator('[data-testid="tweaks-close"]').click()
    await expect(panel).toHaveCount(0)
  })

  test('q and Esc both close the panel (closeAllModals)', async ({ page }) => {
    // AppShell's global onKey routes both `q` and `Escape` to closeAllModals,
    // which resets tweaksOpenSignal. Verify each independently.
    await openTweaks(page)
    await page.keyboard.press('q')
    await expect(page.locator('[data-testid="tweaks-panel"]')).toHaveCount(0)

    await openTweaks(page)
    await page.keyboard.press('Escape')
    await expect(page.locator('[data-testid="tweaks-panel"]')).toHaveCount(0)
  })

  test('accent swatch sets data-accent on <html>/<body> and persists across reload', async ({ page }) => {
    // Default accent is blue (uiState.js fallback).
    await expect(page.locator('html')).toHaveAttribute('data-accent', 'blue')
    await openTweaks(page)
    await page.locator('[data-testid="tweaks-accent-amber"]').click()
    await expect(page.locator('html')).toHaveAttribute('data-accent', 'amber')
    await expect(page.locator('body')).toHaveAttribute('data-accent', 'amber')
    await expect(page.locator('[data-testid="tweaks-accent-amber"]')).toHaveClass(/\bon\b/)
    // uiState persist() JSON-encodes the signal value.
    expect(await lsValue(page, 'agentdeck.accent')).toBe('"amber"')

    await page.reload()
    await waitForTopbar(page)
    await expect(page.locator('html')).toHaveAttribute('data-accent', 'amber')
    await openTweaks(page)
    await expect(page.locator('[data-testid="tweaks-accent-amber"]')).toHaveClass(/\bon\b/)
  })

  test('density seg button sets data-density and persists across reload', async ({ page }) => {
    // Default density is balanced (uiState.js fallback).
    await expect(page.locator('html')).toHaveAttribute('data-density', 'balanced')
    await openTweaks(page)
    await page.locator('[data-testid="tweaks-density-compact"]').click()
    await expect(page.locator('html')).toHaveAttribute('data-density', 'compact')
    await expect(page.locator('body')).toHaveAttribute('data-density', 'compact')
    await expect(page.locator('[data-testid="tweaks-density-compact"]')).toHaveClass(/\bon\b/)
    expect(await lsValue(page, 'agentdeck.density')).toBe('"compact"')

    await page.reload()
    await waitForTopbar(page)
    await expect(page.locator('html')).toHaveAttribute('data-density', 'compact')
    await openTweaks(page)
    await expect(page.locator('[data-testid="tweaks-density-compact"]')).toHaveClass(/\bon\b/)
  })
})

test.describe('right rail (desktop/tablet)', () => {
  // app.css hides .rightrail entirely below 720px (`.sidebar, .rightrail,
  // .footer { display: none; }`), so rail visibility is not observable on the
  // phone project. Same ≥768 cutoff as keyboard-parity.spec.js.
  test.skip(({ viewport }) => (viewport?.width || 1280) < 768, 'phone viewport: right rail is display:none on phones')

  test.beforeEach(async ({ page, request }) => {
    await request.post('/__fixture/reset')
  })

  test('tweaks rail switch hides/shows the rail and persists', async ({ page }) => {
    await page.goto('/')
    await waitForAppMount(page)
    const rail = page.locator('[data-testid="right-rail"]')
    await expect(rail).toBeVisible()

    await openTweaks(page)
    await page.locator('[data-testid="tweaks-rail-switch"]').click()
    // railSignal 'hidden' → body[data-rail="hidden"] → .rightrail display:none.
    await expect(rail).toBeHidden()
    await expect(page.locator('body')).toHaveAttribute('data-rail', 'hidden')
    await expect(page.locator('[data-testid="tweaks-panel"]')).toContainText('hidden')
    expect(await lsValue(page, 'agentdeck.rail')).toBe('"hidden"')

    // Survives reload.
    await page.reload()
    await waitForTopbar(page)
    await expect(page.locator('[data-testid="right-rail"]')).toBeHidden()

    // Switch back on.
    await openTweaks(page)
    await page.locator('[data-testid="tweaks-rail-switch"]').click()
    await expect(page.locator('[data-testid="right-rail"]')).toBeVisible()
    expect(await lsValue(page, 'agentdeck.rail')).toBe('"visible"')
  })

  test('topbar ] icon button toggles the rail', async ({ page }) => {
    await page.goto('/')
    await waitForAppMount(page)
    const railBtn = page.locator('button[aria-label="Toggle right rail"]')
    const rail = page.locator('[data-testid="right-rail"]')
    await expect(rail).toBeVisible()

    await railBtn.click()
    await expect(rail).toBeHidden()
    await expect(page.locator('body')).toHaveAttribute('data-rail', 'hidden')

    await railBtn.click()
    await expect(rail).toBeVisible()
    await expect(page.locator('body')).toHaveAttribute('data-rail', 'visible')
  })

  test('panel card toggles hide/show cards and persist (agentdeck.rightRailPanels)', async ({ page }) => {
    await page.goto('/s/sess-001')
    await waitForAppMount(page)
    const overview = page.locator('[data-testid="rail-card-overview"]')
    const usage = page.locator('[data-testid="rail-card-usage"]')
    await expect(overview).toBeVisible()
    await expect(usage).toBeVisible()

    // Toggle Overview off via the rail-add picker.
    await page.locator('[data-testid="rail-panel-toggle-overview"]').click()
    await expect(overview).toHaveCount(0)
    await expect(usage).toBeVisible() // other cards untouched
    const stored = JSON.parse(await lsValue(page, 'agentdeck.rightRailPanels'))
    expect(stored.overview).toBe(false)
    expect(stored.usage).toBe(true)

    // Persists across reload.
    await page.reload()
    await waitForAppMount(page)
    await expect(page.locator('[data-testid="rail-card-overview"]')).toHaveCount(0)

    // Toggle back on.
    await page.locator('[data-testid="rail-panel-toggle-overview"]').click()
    await expect(page.locator('[data-testid="rail-card-overview"]')).toBeVisible()
  })

  test('MCPs and Skills cards can be toggled off independently', async ({ page }) => {
    await page.goto('/s/sess-001')
    await waitForAppMount(page)
    await expect(page.locator('[data-testid="rail-card-mcps"]')).toBeVisible()
    await expect(page.locator('[data-testid="rail-card-skills"]')).toBeVisible()

    await page.locator('[data-testid="rail-panel-toggle-mcps"]').click()
    await expect(page.locator('[data-testid="rail-card-mcps"]')).toHaveCount(0)
    await expect(page.locator('[data-testid="rail-card-skills"]')).toBeVisible()

    await page.locator('[data-testid="rail-panel-toggle-skills"]').click()
    await expect(page.locator('[data-testid="rail-card-skills"]')).toHaveCount(0)

    const stored = JSON.parse(await lsValue(page, 'agentdeck.rightRailPanels'))
    expect(stored.mcps).toBe(false)
    expect(stored.skills).toBe(false)
    expect(stored.overview).toBe(true)
  })

  test('Overview card shows seeded fields for sess-001', async ({ page }) => {
    // Grounded in fixtures/cmd/web-fixture/main.go seed(): sess-001 is
    // Title "agent-deck", Tool "claude", GroupPath "work",
    // ProjectPath "/srv/agent-deck", Status idle.
    await page.goto('/s/sess-001')
    await waitForAppMount(page)
    const rail = page.locator('[data-testid="right-rail"]')
    await expect(rail.locator('.rail-head')).toContainText('agent-deck')
    const overview = page.locator('[data-testid="rail-card-overview"]')
    await expect(overview).toBeVisible()
    await expect(overview).toContainText('claude')          // tool
    await expect(overview).toContainText('work')            // group
    await expect(overview).toContainText('/srv/agent-deck') // path
    await expect(overview.locator('.pill')).toContainText('idle') // status badge
  })
})
