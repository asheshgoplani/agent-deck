// e2e/group-selection.spec.js -- Group rows are selectable, and selecting one
// swaps the main work area for the group stats panel.
//
// Fixture seed (tests/web/fixtures/cmd/web-fixture/main.go seed()):
//   sess-001 "agent-deck"    tool=claude status=idle    group=work
//   sess-002 "frontend"      tool=claude status=running group=work
//   sess-003 "innotrade-api" tool=codex  status=idle    group=work/innotrade
//   sess-004 "scratch"       tool=shell  status=idle    group=personal
//
// Phone (<768px) skips: the sidebar is desktop/tablet-only.
import { test, expect } from '@playwright/test'

test.describe('group selection', () => {
  test.beforeEach(async ({ page, viewport }) => {
    test.skip(!!viewport && viewport.width < 768, 'sidebar is desktop/tablet-only')
    await page.goto('/')
    await expect(page.locator('.sess')).toHaveCount(4, { timeout: 5000 })
  })

  test('clicking the group name selects it without collapsing', async ({ page }) => {
    const head = page.locator('[data-testid="group-head-work"]')

    await head.locator('.name').click()

    await expect(head).toHaveClass(/\bsel\b/)
    await expect(head.locator('.chev')).toHaveText('▾')
    await expect(page.locator('.sess')).toHaveCount(4)
  })

  test('clicking the chevron collapses without selecting', async ({ page }) => {
    const head = page.locator('[data-testid="group-head-work"]')

    await page.locator('[data-testid="group-chev-work"]').click()

    await expect(head.locator('.chev')).toHaveText('▸')
    await expect(page.locator('.sess')).toHaveCount(2)
    await expect(head).not.toHaveClass(/\bsel\b/)
  })

  test('selecting a session clears the group selection', async ({ page }) => {
    const head = page.locator('[data-testid="group-head-work"]')

    await head.locator('.name').click()
    await expect(head).toHaveClass(/\bsel\b/)

    await page.locator('.sess', { hasText: 'agent-deck' }).first().click()

    await expect(head).not.toHaveClass(/\bsel\b/)
  })

  test('collapse survives a reload', async ({ page }) => {
    await page.locator('[data-testid="group-chev-work"]').click()
    await expect(page.locator('.sess')).toHaveCount(2)

    await page.goto('/')
    await expect(page.locator('[data-testid="group-head-work"] .chev')).toHaveText('▸', { timeout: 5000 })
    await expect(page.locator('.sess')).toHaveCount(2)
  })

  test('selecting a group shows its stats panel', async ({ page }) => {
    await page.locator('[data-testid="group-head-work"] .name').click()

    const panel = page.locator('[data-testid="group-stats-panel"]')
    await expect(panel).toBeVisible()
    await expect(panel).toContainText('📁')
    await expect(panel).toContainText('work')

    // "work" has sess-001 (idle) and sess-002 (running) as direct members.
    // work/innotrade is a separate group path and must NOT roll up.
    await expect(page.locator('[data-testid="group-stats-total"]')).toHaveText('2 sessions')

    const fragments = page.locator('[data-testid="group-stats-fragments"]')
    await expect(fragments).toContainText('● 1 running')
    await expect(fragments).toContainText('○ 1 idle')

    // Both members are listed with their tool.
    await expect(panel.locator('.gs-row')).toHaveCount(2)
    await expect(panel.locator('.gs-row')).toContainText(['agent-deck', 'frontend'])
  })

  test('clicking a session in the stats panel opens it', async ({ page }) => {
    await page.locator('[data-testid="group-head-work"] .name').click()
    await page.locator('[data-testid="group-stats-panel"] .gs-row', { hasText: 'agent-deck' }).click()

    await expect(page.locator('[data-testid="group-stats-panel"]')).toHaveCount(0)
    await expect(page.locator('[data-testid="group-head-work"]')).not.toHaveClass(/\bsel\b/)
  })

  test('the right rail does not show an unrelated session while a group is selected', async ({ page }) => {
    await page.locator('[data-testid="group-head-work"] .name').click()
    await expect(page.locator('[data-testid="right-rail"]')).toContainText('group selected')
  })
})
