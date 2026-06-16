// e2e/genui.spec.js -- Generative Command Center (v-genui-0) end-to-end.
//
// The GenUI pane (internal/web/static/app/panes/GenuiPane.js) renders the SAME
// live fleet snapshot through 3 hand-authored, server-VALIDATED whole-UI specs.
// Switching specs reshapes the whole UI live (no rebuild). The fixed renderer
// (GenuiRenderer.js) + the Go validator are the trusted engine that makes that
// safe — a spec is DATA the renderer interprets, never code the browser runs.
//
// These tests prove, against the fixture web server:
//   - the 3 server-validated view specs are served and listed,
//   - a view renders from a spec (real fleet snapshot populates it),
//   - RESHAPE: switching a view re-renders the whole UI live,
//   - the renderer never executes spec content (the security keystone is
//     covered exhaustively in the vitest unit suite + the Go validator).
import { test, expect } from '@playwright/test'

async function openGenui(page) {
  await page.goto('/')
  const viewport = page.viewportSize()
  if (viewport && viewport.width < 768) {
    // Phone viewports use the bottom mobile tab bar; the GenUI tab is desktop-
    // only in v0, so widen and use the top strip.
    await page.setViewportSize({ width: 1280, height: 900 })
  }
  await page.locator('.top-tab', { hasText: 'GenUI' }).click()
  await expect(page.locator('[data-testid="genui-pane"]')).toBeVisible({ timeout: 5000 })
}

test.describe('generative command center (genui-0)', () => {
  test.beforeEach(async ({ request }) => {
    await request.post('/__fixture/reset')
  })

  test('serves the three validated view specs', async ({ request }) => {
    const res = await request.get('/api/command-center/genui/views')
    expect(res.ok()).toBeTruthy()
    const body = await res.json()
    const ids = body.views.map(v => v.id)
    expect(ids).toEqual(['status-board', 'blocked-first', 'by-project'])

    for (const id of ids) {
      const sres = await request.get('/api/command-center/genui/spec/' + id)
      expect(sres.ok()).toBeTruthy()
      const spec = await sres.json()
      expect(spec.specId).toBe(id)
      expect(spec.root).toBeTruthy()
    }
  })

  test('an unknown view id is 404', async ({ request }) => {
    const res = await request.get('/api/command-center/genui/spec/does-not-exist')
    expect(res.status()).toBe(404)
  })

  test('renders the default status-board view from its spec', async ({ page }) => {
    await openGenui(page)
    // The renderer mounts the spec root.
    await expect(page.locator('[data-testid="genui-root"]')).toHaveAttribute('data-spec-id', 'status-board', { timeout: 5000 })
    // Stat widgets bound by reference to the live totals.
    await expect(page.locator('[data-testid="genui-stat"]').first()).toBeVisible()
    // The status-list populates from the live fleet snapshot (≥1 conductor).
    await expect(page.locator('[data-testid="genui-status-row"]').first()).toBeVisible({ timeout: 5000 })
  })

  test('RESHAPE: switching the view re-renders the whole UI live', async ({ page }) => {
    await openGenui(page)
    // Start on status-board (status rows, no decision list).
    await expect(page.locator('[data-testid="genui-root"]')).toHaveAttribute('data-spec-id', 'status-board', { timeout: 5000 })
    await expect(page.locator('[data-testid="genui-status-list"]')).toBeVisible()
    await expect(page.locator('[data-testid="genui-decision-list"]')).toHaveCount(0)

    // Switch to blocked-first -> the WHOLE UI reshapes to a decision-led view.
    await page.locator('[data-testid="genui-view-blocked-first"]').click()
    await expect(page.locator('[data-testid="genui-root"]')).toHaveAttribute('data-spec-id', 'blocked-first', { timeout: 5000 })
    await expect(page.locator('[data-testid="genui-decision-list"]')).toBeVisible()

    // Switch to by-project -> a grid of conductor cards via the repeat primitive.
    await page.locator('[data-testid="genui-view-by-project"]').click()
    await expect(page.locator('[data-testid="genui-root"]')).toHaveAttribute('data-spec-id', 'by-project', { timeout: 5000 })
    await expect(page.locator('[data-testid="genui-conductor-card"]').first()).toBeVisible({ timeout: 5000 })
  })

  test('security: a spec text field with markup is escaped, not executed', async ({ page }) => {
    await openGenui(page)
    // Render a malicious spec directly through the loaded renderer and assert
    // no code ran and no markup nodes were injected. (The full matrix lives in
    // the vitest unit suite; this is the in-browser smoke against the real app.)
    const result = await page.evaluate(async () => {
      window.__XSS_FIRED__ = false
      const [{ renderSpec }, preact] = await Promise.all([
        import('/static/app/genui/GenuiRenderer.js'),
        import('/static/vendor/preact.mjs'),
      ])
      const evil = '<' + 'script>window.__XSS_FIRED__=true</' + 'script><img src=x onerror=window.__XSS_FIRED__=true>'
      const spec = { schema: 1, specId: 'evil', title: 't', root: { type: 'text', text: evil } }
      const host = document.createElement('div')
      document.body.appendChild(host)
      preact.render(renderSpec(spec, {}), host)
      await new Promise(r => setTimeout(r, 100))
      const out = {
        fired: window.__XSS_FIRED__,
        scripts: host.querySelectorAll('script').length,
        imgs: host.querySelectorAll('img').length,
        escaped: host.textContent.includes('script'),
      }
      host.remove()
      return out
    })
    expect(result.fired).toBe(false)
    expect(result.scripts).toBe(0)
    expect(result.imgs).toBe(0)
    expect(result.escaped).toBe(true)
  })
})

// --- genui-1: the LLM emits the validated spec ----------------------------
test.describe('generative command center (genui-1) — intent → compose → render', () => {
  test.beforeEach(async ({ request }) => {
    await request.post('/__fixture/reset')
  })

  test('the compose endpoint returns a server-validated spec', async ({ request }) => {
    const res = await request.post('/api/command-center/genui/compose', {
      data: { intent: "show me what's blocked" },
    })
    expect(res.ok()).toBeTruthy()
    const body = await res.json()
    // The composer emitted a spec and the trace says which composer did it.
    expect(body.spec).toBeTruthy()
    expect(body.spec.specId).toBe('composed-blocked')
    expect(body.trace.composer).toBe('stub')
  })

  test('a rejected intent returns a CLEAN error and NO spec', async ({ request }) => {
    // The stub emits an unknown-widget spec for this probe; the unchanged
    // validator rejects it across the repair budget → clean 422, no spec.
    const res = await request.post('/api/command-center/genui/compose', {
      data: { intent: 'please use an unknown widget' },
    })
    expect(res.status()).toBe(422)
    const body = await res.json()
    expect(body.code).toBe('COMPOSE_FAILED')
    expect(body.spec).toBeUndefined() // never return unvalidated output
  })

  test('typing an intent composes a spec and reshapes the whole UI', async ({ page }) => {
    await openGenui(page)
    await expect(page.locator('[data-testid="genui-root"]')).toHaveAttribute('data-spec-id', 'status-board', { timeout: 5000 })

    // Type an intent and compose. The LLM (stub) emits a blocked-first spec.
    await page.locator('[data-testid="genui-intent-input"]').fill("show me what's blocked")
    await page.locator('[data-testid="genui-intent-go"]').click()

    // The WHOLE UI is now the composed spec (decision-list present), with a trace.
    await expect(page.locator('[data-testid="genui-root"]')).toHaveAttribute('data-spec-id', 'composed-blocked', { timeout: 5000 })
    await expect(page.locator('[data-testid="genui-decision-list"]')).toBeVisible()
    await expect(page.locator('[data-testid="genui-trace"]')).toContainText('composed by')

    // A different intent composes a different whole UI (grouped grid).
    await page.locator('[data-testid="genui-intent-input"]').fill('group everything by project')
    await page.locator('[data-testid="genui-intent-go"]').click()
    await expect(page.locator('[data-testid="genui-root"]')).toHaveAttribute('data-spec-id', 'composed-by-project', { timeout: 5000 })
    await expect(page.locator('[data-testid="genui-conductor-card"]').first()).toBeVisible({ timeout: 5000 })
  })

  test('a rejected compose shows a clean error and renders nothing unvalidated', async ({ page }) => {
    await openGenui(page)
    await expect(page.locator('[data-testid="genui-root"]')).toBeVisible({ timeout: 5000 })

    await page.locator('[data-testid="genui-intent-input"]').fill('please use an unknown widget')
    await page.locator('[data-testid="genui-intent-go"]').click()

    // Clean error surfaces; no genui-root (no unvalidated spec) is rendered.
    await expect(page.locator('[data-testid="genui-load-error"]')).toBeVisible({ timeout: 5000 })
    await expect(page.locator('[data-testid="genui-root"]')).toHaveCount(0)
  })
})
