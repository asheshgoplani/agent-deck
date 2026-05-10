// e2e/smoke.spec.js -- the floor: confirms boot, asset serving, and the
// shell renders against the in-memory fixture. If this fails, every other
// e2e is meaningless, so we keep it minimal and dependency-free.

import { test, expect } from '@playwright/test'

test.describe('smoke', () => {
  test('healthz reports ok', async ({ request }) => {
    const res = await request.get('/healthz')
    expect(res.ok()).toBe(true)
    const body = await res.json()
    expect(body.ok).toBe(true)
    expect(body.profile).toBe('fixture')
  })

  test('index loads and includes Preact entry', async ({ page }) => {
    const response = await page.goto('/')
    expect(response?.ok()).toBe(true)
    // The bundled index uses {{ASSET:app/main.js}} resolved at request time;
    // verify the resolved <script type="module" src="/static/...main.js">.
    const moduleScripts = await page.locator('script[type="module"]').all()
    expect(moduleScripts.length).toBeGreaterThan(0)
    const importmap = await page.locator('script[type="importmap"]').textContent()
    expect(importmap).toContain('"preact"')
    expect(importmap).toContain('"htm/preact"')
  })

  test('static assets respond 200 (no broken imports)', async ({ request }) => {
    const paths = [
      '/static/vendor/preact.mjs',
      '/static/vendor/preact-hooks.mjs',
      '/static/vendor/htm.mjs',
      '/static/vendor/signals.mjs',
      '/manifest.webmanifest',
    ]
    for (const p of paths) {
      const res = await request.get(p)
      expect(res.status(), `${p} should be 200`).toBe(200)
    }
  })

  test('app shell renders with seeded sessions', async ({ page }) => {
    await page.goto('/')
    // Wait for the Preact root to populate. The exact selector is
    // implementation-detail, but the document body should mount something
    // beyond the <noscript> shell within 5s.
    await page.waitForFunction(() => {
      const root = document.querySelector('#app, .app, [data-testid="app-root"], main')
      return root && root.textContent && root.textContent.trim().length > 0
    }, { timeout: 5000 })
    // Confirm the seeded session titles eventually surface somewhere.
    const html = await page.content()
    expect(html.toLowerCase()).toContain('agent-deck')
  })
})
