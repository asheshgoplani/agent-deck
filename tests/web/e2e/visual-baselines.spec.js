// e2e/visual-baselines.spec.js -- screenshot regression baselines.
//
// PR-A captures the CURRENT (pre-redesign) appearance as the baseline so
// PR-B's visual changes show up as intentional diffs against it. When PR-B
// lands the redesign, regenerate with `npm run test:e2e:update-snapshots`
// and review every diff.
//
// Animations are disabled at the config level so baselines are stable.

import { test, expect } from '@playwright/test'

async function waitForAppMount(page) {
  await page.waitForFunction(() => {
    const root = document.querySelector('#app, .app, [data-testid="app-root"], main')
    return root && root.textContent && root.textContent.trim().length > 50
  }, { timeout: 5000 })
}

test.describe('visual baselines', () => {
  test.beforeEach(async ({ request }) => {
    await request.post('/__fixture/reset')
  })

  test('home: index loaded with seeded sessions', async ({ page }) => {
    await page.goto('/')
    await waitForAppMount(page)
    // Allow the SSE menu subscription handshake to settle so the screenshot
    // is deterministic.
    await page.waitForTimeout(300)
    await expect(page).toHaveScreenshot('home.png', { fullPage: false })
  })

  test('home: dark theme', async ({ page }) => {
    await page.goto('/')
    await waitForAppMount(page)
    await page.evaluate(() => {
      document.documentElement.dataset.theme = 'dark'
    })
    await page.waitForTimeout(300)
    await expect(page).toHaveScreenshot('home-dark.png', { fullPage: false })
  })

  test('home: empty state (after deleting all sessions via web)', async ({
    page,
    request,
  }) => {
    // Delete every seeded session by id, then reload.
    const snap = await (await request.get('/__fixture/snapshot')).json()
    for (const item of snap.items || []) {
      if (item.type === 'session' && item.session) {
        await request.delete(`/api/sessions/${item.session.id}`)
      }
    }
    await page.goto('/')
    await waitForAppMount(page)
    await page.waitForTimeout(300)
    await expect(page).toHaveScreenshot('home-empty.png', { fullPage: false })
  })
})
