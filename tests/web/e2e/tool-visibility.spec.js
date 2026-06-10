// e2e/tool-visibility.spec.js -- tool-visibility denylist through the live web stack.
//
// Exercises GET /api/settings → AppShell hydration → CreateSessionDialog picker.
// Uses the host process registry (same as production); assertions are written
// relative to the settings response so they stay deterministic on CI.

import { test, expect } from '@playwright/test'

async function resetFixture(request) {
  const res = await request.post('/__fixture/reset')
  expect(res.status()).toBe(204)
}

test.describe('tool visibility — settings API', () => {
  test.beforeEach(async ({ request }) => {
    await resetFixture(request)
  })

  test('GET /api/settings returns hiddenTools and pickerTools arrays', async ({ request }) => {
    const res = await request.get('/api/settings')
    expect(res.ok()).toBe(true)
    const body = await res.json()
    expect(Array.isArray(body.hiddenTools)).toBe(true)
    expect(Array.isArray(body.pickerTools)).toBe(true)
    expect(body.pickerTools.length).toBeGreaterThan(0)
    expect(body.pickerTools).toContain('shell')
    for (const hidden of body.hiddenTools) {
      expect(body.pickerTools).not.toContain(hidden)
    }
  })
})

test.describe('tool visibility — new-session picker UI', () => {
  test.skip(({ viewport }) => (viewport?.width || 1280) < 768, 'phone viewport: picker UI is desktop/tablet only')
  test.beforeEach(async ({ request }) => {
    await resetFixture(request)
  })

  test('CreateSessionDialog shows pickerTools and always includes shell', async ({ page, request }) => {
    const settings = await request.get('/api/settings').then((r) => r.json())

    await page.goto('/')
    await page.waitForSelector('.sess', { timeout: 5000 })
    await page.keyboard.press('n')
    await expect(page.locator('.overlay .dialog')).toBeVisible()

    const labels = await page.locator('.seg-row .seg-btn').allTextContents()
    const normalized = labels.map((t) => t.trim())

    expect(normalized).toContain('shell')
    for (const tool of settings.pickerTools) {
      const display = tool === 'codex' ? 'ChatGPT' : tool
      expect(normalized).toContain(display)
    }
    for (const hidden of settings.hiddenTools) {
      expect(normalized).not.toContain(hidden)
    }
  })
})
