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

  test('member rows show a sized status dot', async ({ page }) => {
    await page.locator('[data-testid="group-head-work"] .name').click()
    const dot = page.locator('[data-testid="group-stats-panel"] .gs-row .dot').first()
    await expect(dot).toBeVisible()
    const box = await dot.boundingBox()
    expect(box.width).toBeGreaterThan(0)
    expect(box.height).toBeGreaterThan(0)

    // The Dot component sets width/height as an inline style (icons.js), so
    // the boundingBox checks above pass even with zero matching CSS rules --
    // they only catch a display:none regression, not the missing-styling bug
    // this test is meant to catch. Shape and color come ONLY from the
    // .group-stats .gs-row .dot CSS rules, so assert those directly: an
    // unstyled span has no border-radius and a transparent background.
    const shape = await dot.evaluate((el) => {
      const cs = getComputedStyle(el)
      return { borderRadius: cs.borderRadius, background: cs.backgroundColor }
    })
    expect(shape.borderRadius).not.toBe('0px')
    expect(shape.background).not.toBe('rgba(0, 0, 0, 0)')
  })

  test('new session from a group prefills the group folder and tool', async ({ page }) => {
    await page.locator('[data-testid="group-head-work"] .name').click()
    await page.locator('[data-testid="group-new-session-btn"]').click()

    // Fixture: group "work" has DefaultPath "/srv/work"; its newest session
    // (sess-002 "frontend") uses claude.
    await expect(page.locator('[data-testid="create-session-group"]')).toHaveText('work')
    await expect(page.locator('.dialog input').nth(1)).toHaveValue('/srv/work')
    await expect(page.locator('.dialog .seg-btn.on')).toHaveText('claude')
  })

  test('a group with no configured folder falls back to its newest session path', async ({ page }) => {
    await page.locator('[data-testid="group-head-personal"] .name').click()
    await page.locator('[data-testid="group-new-session-btn"]').click()

    // Fixture: "personal" has no DefaultPath; sess-004 "scratch" is its only
    // session (ProjectPath "/home/dev/scratch", tool=shell), so the newest-
    // session fallback supplies both the folder and the tool.
    await expect(page.locator('[data-testid="create-session-group"]')).toHaveText('personal')
    await expect(page.locator('.dialog input').nth(1)).toHaveValue('/home/dev/scratch')
  })

  test('j walks group headers as well as sessions', async ({ page }) => {
    await page.locator('body').click()

    // Rendered order: g:work, s:sess-001, s:sess-002, g:work/innotrade,
    // s:sess-003, g:personal, s:sess-004.
    await page.keyboard.press('j')
    await expect(page.locator('[data-testid="group-head-work"]')).toHaveClass(/\bsel\b/)

    await page.keyboard.press('j')
    await expect(page.locator('[data-testid="group-head-work"]')).not.toHaveClass(/\bsel\b/)
    await expect(page.locator('.sess.sel .tt')).toHaveText('agent-deck')

    await page.keyboard.press('k')
    await expect(page.locator('[data-testid="group-head-work"]')).toHaveClass(/\bsel\b/)
  })

  test('arrow keys collapse and expand the focused group', async ({ page }) => {
    await page.locator('[data-testid="group-head-work"] .name').click()

    await page.keyboard.press('ArrowLeft')
    await expect(page.locator('[data-testid="group-head-work"] .chev')).toHaveText('▸')
    await expect(page.locator('.sess')).toHaveCount(2)

    await page.keyboard.press('ArrowRight')
    await expect(page.locator('[data-testid="group-head-work"] .chev')).toHaveText('▾')
    await expect(page.locator('.sess')).toHaveCount(4)
  })

  test('n opens the dialog prefilled from the focused group', async ({ page }) => {
    await page.locator('[data-testid="group-head-work"] .name').click()
    await page.keyboard.press('n')

    await expect(page.locator('[data-testid="create-session-group"]')).toHaveText('work')
  })

  test('n after keyboard-navigating to a group prefills from that group', async ({ page }) => {
    await page.locator('body').click()

    // Keyboard only -- no click on the group row. Rendered order puts the
    // `work` header first, so a single j focuses it.
    await page.keyboard.press('j')
    await expect(page.locator('[data-testid="group-head-work"]')).toHaveClass(/\bsel\b/)

    await page.keyboard.press('n')

    await expect(page.locator('[data-testid="create-session-group"]')).toHaveText('work')
    await expect(page.locator('.dialog input').nth(1)).toHaveValue('/srv/work')
  })

  test('group selection is reflected in the URL and survives a reload', async ({ page }) => {
    await page.locator('[data-testid="group-head-work"] .name').click()
    await expect(page).toHaveURL(/\/g\/work$/)

    await page.reload()
    await expect(page.locator('[data-testid="group-stats-panel"]')).toBeVisible({ timeout: 5000 })
    await expect(page.locator('[data-testid="group-head-work"]')).toHaveClass(/\bsel\b/)
  })

  test('a nested group path round-trips through the URL', async ({ page }) => {
    await page.locator('[data-testid="group-head-work/innotrade"] .name').click()
    await expect(page).toHaveURL(/\/g\/work%2Finnotrade$/)

    await page.reload()
    await expect(page.locator('[data-testid="group-stats-panel"]')).toHaveAttribute('data-group-path', 'work/innotrade', { timeout: 5000 })
  })

  // Final-review finding #1: Tab became a global keyboard trap whenever a
  // group was selected. The old guard exempted only INPUT/TEXTAREA/SELECT/
  // contenteditable, so once focus landed on a <button> (any button,
  // anywhere on the page) the next Tab press was swallowed by the group
  // toggle instead of advancing focus -- including inside an open dialog.
  test('Tab does not trap focus or toggle the group behind an open dialog', async ({ page }) => {
    await page.locator('[data-testid="group-head-work"] .name').click()
    await expect(page.locator('[data-testid="group-head-work"] .chev')).toHaveText('▾')

    await page.locator('[data-testid="group-new-session-btn"]').click()
    await expect(page.locator('.overlay .dialog')).toBeVisible()

    // Focus a <button> inside the dialog -- e.target on the next keydown is
    // then a button, not an exempted form control.
    const toolBtn = page.locator('.dialog .seg-btn').first()
    await toolBtn.focus()
    await expect(toolBtn).toBeFocused()

    await page.keyboard.press('Tab')

    // Focus must advance off the button rather than staying pinned to it.
    await expect(toolBtn).not.toBeFocused()
    // The dialog stays open and the group behind it stays expanded -- proof
    // the keystroke was not silently consumed by the group toggle.
    await expect(page.locator('.overlay .dialog')).toBeVisible()
    await expect(page.locator('[data-testid="group-head-work"] .chev')).toHaveText('▾')
  })

  // Once a Service Worker is active, its own fetch() calls originate outside
  // the page's network stack, so page.route() cannot see or mock them (see
  // session-actions-ui.spec.js's identical note on the canFork /api/menu
  // test). Scoped to just this test so it stays deterministic without
  // disabling the SW for the rest of the file.
  test.describe('settings picker-tools override (SW blocked for deterministic routing)', () => {
    test.use({ serviceWorkers: 'block' })

    // Final-review finding #2: the dialog seeded a tool from the group's
    // newest session even when an operator-restricted picker (hidden_tools /
    // show_only_installed_tools) does not show it -- no button appeared
    // selected, yet submit silently posted the hidden tool anyway.
    test('the new-session dialog never seeds a tool the picker hides', async ({ page }) => {
      // Mirrors tool-visibility.spec.js's GET /api/settings -> picker-UI
      // pattern, but forces a restricted pickerTools so the seed (group
      // "work"'s newest session, sess-002, uses claude) is provably not shown.
      await page.route('**/api/settings', async (route) => {
        const response = await route.fetch()
        const body = await response.json()
        body.pickerTools = ['codex', 'shell']
        await route.fulfill({ response, json: body })
      })

      await page.goto('/')
      await expect(page.locator('.sess')).toHaveCount(4, { timeout: 5000 })

      await page.locator('[data-testid="group-head-work"] .name').click()
      await page.keyboard.press('n')
      await expect(page.locator('.overlay .dialog')).toBeVisible()

      const shown = (await page.locator('.dialog .seg-row .seg-btn').allTextContents()).map((t) => t.trim())
      expect(shown).toEqual(['ChatGPT', 'shell'])

      // Exactly one button reflects the actual selection, and it must be a
      // shown tool -- never the hidden `claude` the group would otherwise seed.
      const selected = page.locator('.dialog .seg-btn.on')
      await expect(selected).toHaveCount(1)
      await expect(selected).toHaveText('ChatGPT')

      // Submitting must post the tool the picker actually shows. Intercept
      // and fulfill POST /api/sessions ourselves (rather than letting it hit
      // the shared fixture) so this test does not leave a stray session
      // behind for the other tests in this file, none of which reset the
      // fixture between tests.
      let posted = null
      await page.route('**/api/sessions', async (route) => {
        if (route.request().method() !== 'POST') return route.continue()
        posted = route.request().postDataJSON()
        await route.fulfill({ status: 201, contentType: 'application/json', body: JSON.stringify({ id: 'fake-finding2-repro' }) })
      })
      await page.locator('.dialog input').first().fill('finding2-repro')
      await page.locator('.dialog button[type=submit]').click()
      await expect(page.locator('.overlay .dialog')).toHaveCount(0)
      expect(posted?.tool).toBe('codex')
    })
  })

  // Final-review finding #3: a group that vanishes while selected (deleted
  // in the TUI, a stale /g/{path} URL/reload) left the stats panel rendering
  // fabricated "0 sessions" with a working create button -- groupCreateDefaults
  // returns the blank context for an unknown group, so that button silently
  // created in the default group instead of the one shown on screen.
  test('a stale group selection offers no create button and no fabricated stats', async ({ page }) => {
    // Cold navigation to /g/does-not-exist would otherwise leave the main
    // area on the persisted-independent "fleet" tab; select a real group
    // first (persists activeTabSignal='terminal') so the repro matches the
    // real scenario -- selected in the browser, then the group disappears.
    await page.locator('[data-testid="group-head-work"] .name').click()
    await expect(page.locator('[data-testid="group-stats-panel"]')).toBeVisible()

    await page.goto('/g/does-not-exist')

    const panel = page.locator('[data-testid="group-stats-panel"]')
    await expect(panel).toBeVisible({ timeout: 5000 })
    await expect(panel).toContainText('does-not-exist')
    await expect(page.locator('[data-testid="group-stats-missing"]')).toBeVisible()

    await expect(page.locator('[data-testid="group-stats-total"]')).toHaveCount(0)
    await expect(page.locator('[data-testid="group-new-session-btn"]')).toHaveCount(0)
  })
})
