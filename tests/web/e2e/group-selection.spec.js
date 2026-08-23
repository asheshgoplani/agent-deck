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
    await page.keyboard.press('n')

    // Fixture: group "work" has DefaultPath "/srv/work"; its newest session
    // (sess-002 "frontend") uses claude.
    await expect(page.locator('[data-testid="create-session-group"]')).toHaveText('work')
    await expect(page.locator('.dialog input').nth(1)).toHaveValue('/srv/work')
    await expect(page.locator('.dialog .seg-btn.on')).toHaveText('claude')

    // Field naming tracks the TUI's create dialog, which labels this "Name:"
    // with placeholder "session-name" (internal/ui/newdialog.go:336,:2720) --
    // NOT "Title"/"my-session". The TUI's *edit* dialog does say "Title"
    // (edit_session_dialog.go:69), so the two genuinely differ; don't unify.
    await expect(page.locator('.dialog .field:has(input) label').first()).toHaveText('NAME')
    await expect(page.locator('.dialog input').first()).toHaveAttribute('placeholder', 'session-name')
  })

  test('a group with no configured folder falls back to its newest session path', async ({ page }) => {
    await page.locator('[data-testid="group-head-personal"] .name').click()
    await page.keyboard.press('n')

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

  // TUI parity: renderGroupPreview iterates group.Sessions from a tree built
  // over the FULL instance set (home.go:3540), so ARCHIVED sessions appear in
  // the group preview even though the TUI's left list partitions them out.
  // The web snapshot behind the sidebar is archive-filtered server-side, so
  // without groupMembers() folding the archived feed back in, a session
  // archived from the TUI vanished from the web group panel entirely.
  test('the panel lists archived sessions the sidebar omits', async ({ page }) => {
    // Archive sess-001 ("agent-deck", group "work"), then restore it below so
    // sibling tests keep their 4-session fixture assumptions.
    await page.request.post('/api/sessions/sess-001/archive')
    try {
      await page.goto('/')
      // Sidebar drops it -- the active snapshot is archive-filtered.
      await expect(page.locator('.sess')).toHaveCount(3, { timeout: 5000 })
      await expect(page.locator('.sess .tt')).not.toContainText(['agent-deck'])

      await page.locator('[data-testid="group-head-work"] .name').click()
      const panel = page.locator('[data-testid="group-stats-panel"]')
      await expect(panel).toBeVisible()

      // ...but the panel still shows it, flagged, and still counts it.
      const archivedRow = panel.locator('.gs-row.archived')
      await expect(archivedRow).toHaveCount(1)
      await expect(archivedRow).toContainText('agent-deck')
      await expect(archivedRow.locator('.gs-archived')).toHaveText('archived')
      await expect(page.locator('[data-testid="group-stats-total"]')).toHaveText('2 sessions')
    } finally {
      await page.request.post('/api/sessions/sess-001/unarchive')
    }
  })

  // A COLD load -- no prior click, so activeTabSignal is still its persisted
  // default of 'fleet'. This is what a shared /g/{path} link actually does.
  // The existing reload test below cannot catch this: it clicks the group
  // first, and the click sets the tab as a side effect.
  test('a cold /g/{path} link renders the panel, not just the sidebar row', async ({ browser }) => {
    const ctx = await browser.newContext()   // fresh origin storage
    const fresh = await ctx.newPage()
    try {
      await fresh.goto('/g/work')
      await expect(fresh.locator('[data-testid="group-stats-panel"]')).toBeVisible({ timeout: 5000 })
      await expect(fresh.locator('[data-testid="group-stats-total"]')).toHaveText('2 sessions')
      await expect(fresh.locator('[data-testid="group-head-work"]')).toHaveClass(/\bsel\b/)
    } finally {
      await ctx.close()
    }
  })

  test('a cold /s/{id} link renders the terminal pane, not the fleet table', async ({ browser }) => {
    const ctx = await browser.newContext()
    const fresh = await ctx.newPage()
    try {
      await fresh.goto('/s/sess-001')
      await expect(fresh.locator('.term-wrap')).toBeVisible({ timeout: 5000 })
    } finally {
      await ctx.close()
    }
  })

  // PR #2047 review item 2: archived members come from a separate endpoint no
  // SSE event touches, so archiving a session from the selected group left the
  // panel's archived half stale until you navigated away and back.
  test('archiving from the selected group updates the panel without navigating away', async ({ page }) => {
    await page.locator('[data-testid="group-head-work"] .name').click()
    const panel = page.locator('[data-testid="group-stats-panel"]')
    await expect(panel).toBeVisible()
    await expect(page.locator('[data-testid="group-stats-total"]')).toHaveText('2 sessions')
    await expect(panel.locator('.gs-row.archived')).toHaveCount(0)

    await page.request.post('/api/sessions/sess-001/archive')
    try {
      // No reload, no re-selection: the menu SSE event must carry the refresh.
      await expect(panel.locator('.gs-row.archived')).toHaveCount(1, { timeout: 6000 })
      await expect(panel.locator('.gs-row.archived')).toContainText('agent-deck')
      // Still 2 -- one active + one archived; the total counts both, as the TUI does.
      await expect(page.locator('[data-testid="group-stats-total"]')).toHaveText('2 sessions')
      await expect(page.locator('.sess')).toHaveCount(3)
    } finally {
      await page.request.post('/api/sessions/sess-001/unarchive')
    }
  })

  // PR #2047 review item 3: the removed global Tab binding was a WCAG 2.1.2
  // keyboard trap -- forward focus died page-wide whenever a group was
  // selected. This pins that focus escapes the sidebar rather than cycling.
  test('Tab escapes the sidebar while a group is selected', async ({ page }) => {
    await page.locator('[data-testid="group-head-work"] .name').click()
    await expect(page.locator('[data-testid="group-head-work"]')).toHaveClass(/\bsel\b/)

    const inSidebar = () => page.evaluate(() => !!document.activeElement?.closest('.sidebar'))
    const chevron = () => page.locator('[data-testid="group-head-work"] .chev').textContent()

    // Start focus inside the sidebar, on a real control.
    await page.locator('[data-testid="sidebar-filter-input"]').focus()
    await page.keyboard.press('Escape')   // blur the input so Tab is not an in-field key

    let escaped = false
    const before = await chevron()
    for (let i = 0; i < 40 && !escaped; i++) {
      await page.keyboard.press('Tab')
      if (!(await inSidebar())) escaped = true
    }
    expect(escaped, 'focus never left the sidebar within 40 Tab presses').toBe(true)

    // And Tab must not have been hijacked into toggling the group behind it.
    expect(await chevron()).toBe(before)
  })

  // CodeRabbit on PR #2047: anyOverlayOpen() was defined but never called once
  // the Tab binding was removed, so shortcuts still acted on the sidebar behind
  // an open dialog. ConfirmDialog focuses a <button>, not an input, so the
  // in-field guard never applied.
  test('shortcuts do not act behind an open confirm dialog', async ({ page }) => {
    await page.locator('[data-testid="group-head-work"] .name').click()
    await expect(page.locator('[data-testid="group-head-work"]')).toHaveClass(/\bsel\b/)
    const chevBefore = await page.locator('[data-testid="group-head-work"] .chev').textContent()

    // Open a confirm dialog (delete asks first; nothing is mutated).
    await page.locator('.sess', { hasText: 'agent-deck' }).first().hover()
    await page.locator('[data-testid="session-delete-btn"]').first().click()
    const confirm = page.locator('[data-testid="confirm-dialog"], .overlay .dialog')
    await expect(confirm.first()).toBeVisible()

    await page.keyboard.press('n')
    await expect(page.locator('[data-testid="create-session-group"]')).toHaveCount(0)

    await page.keyboard.press('j')
    await page.keyboard.press('ArrowLeft')
    await expect(page.locator('[data-testid="group-head-work"]')).toHaveClass(/\bsel\b/)
    expect(await page.locator('[data-testid="group-head-work"] .chev').textContent()).toBe(chevBefore)

    // `q` must still dismiss it -- that is why the gate is an allow-list.
    await page.keyboard.press('q')
    await expect(confirm.first()).toHaveCount(0)
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

    await page.keyboard.press('n')
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

  // The whole point of this branch: before it, the dialog never sent groupPath,
  // so EVERY session created from the browser landed in the default group. The
  // Go handler always supported it (handlers_sessions_test.go
  // TestSessionsCollectionPOSTForwardsGroupPath) -- what was missing, and what
  // stayed untested until now, is the client actually sending it.
  test('creating from a selected group posts that groupPath', async ({ page }) => {
    await page.locator('[data-testid="group-head-work"] .name').click()
    await page.keyboard.press('n')
    await expect(page.locator('.overlay .dialog')).toBeVisible()

    // Fulfil locally so the shared fixture keeps its 4-session seed.
    let posted = null
    await page.route('**/api/sessions', async (route) => {
      if (route.request().method() !== 'POST') return route.continue()
      posted = route.request().postDataJSON()
      await route.fulfill({ status: 201, contentType: 'application/json', body: JSON.stringify({ id: 'fake-grouppath' }) })
    })
    await page.locator('.dialog input').first().fill('grouppath-probe')
    await page.locator('.dialog button[type=submit]').click()
    await expect(page.locator('.overlay .dialog')).toHaveCount(0)

    expect(posted?.groupPath).toBe('work')
    expect(posted?.projectPath).toBe('/srv/work')
  })

  // ...and the guard on the other side: with no group context the key must be
  // OMITTED, not sent empty, so the server falls back to the default group.
  test('creating with no group selected omits groupPath entirely', async ({ page }) => {
    await page.locator('body').click()
    await page.keyboard.press('n')
    await expect(page.locator('.overlay .dialog')).toBeVisible()
    await expect(page.locator('[data-testid="create-session-group"]')).toHaveCount(0)

    let posted = null
    await page.route('**/api/sessions', async (route) => {
      if (route.request().method() !== 'POST') return route.continue()
      posted = route.request().postDataJSON()
      await route.fulfill({ status: 201, contentType: 'application/json', body: JSON.stringify({ id: 'fake-nogroup' }) })
    })
    await page.locator('.dialog input').first().fill('nogroup-probe')
    await page.locator('.dialog input').nth(1).fill('/tmp/nogroup')
    await page.locator('.dialog button[type=submit]').click()
    await expect(page.locator('.overlay .dialog')).toHaveCount(0)

    expect(posted).not.toBeNull()
    expect('groupPath' in posted).toBe(false)
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

    // `n` on a group that is no longer in the menu must not claim to create in
    // it: groupCreateDefaults() returns the blank context for an unknown path,
    // so the dialog opens with no GROUP row and omits groupPath -- honest about
    // landing in the default group rather than silently lying about the target.
    await page.keyboard.press('n')
    await expect(page.locator('.overlay .dialog')).toBeVisible()
    await expect(page.locator('[data-testid="create-session-group"]')).toHaveCount(0)
  })
})
