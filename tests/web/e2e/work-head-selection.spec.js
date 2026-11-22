// e2e/work-head-selection.spec.js -- the work head's Start/Stop/Restart act
// only on a session the user selected.
//
// Before: with nothing selected (or a /s/{id} link to an unknown id) the head
// fell back to the FIRST session of the list, drew its Start/Restart buttons,
// and a click POSTed .../sess-001/restart for a session the user never chose.
//
// Fixture seed: sess-001 "agent-deck" idle, sess-002 "frontend" running.
// Runs on desktop and phone (390x844); the head is visible on both.
import { test, expect } from '@playwright/test'

test.use({ serviceWorkers: 'block' })

const ACTION_NAMES = /^(Start|Stop|Restart|Fork)$/

test.beforeEach(async ({ page, request }, testInfo) => {
  if (testInfo.project.name === 'chromium-phone') await page.setViewportSize({ width: 390, height: 844 })
  await request.post('/__fixture/reset')
})

function recordActionPosts(page) {
  const posts = []
  page.on('request', req => {
    if (req.method() === 'POST' && /\/api\/sessions\/[^/]+\/(start|stop|restart|fork)$/.test(new URL(req.url()).pathname)) {
      posts.push(new URL(req.url()).pathname)
    }
  })
  return posts
}

async function waitForMenu(page) {
  await page.waitForSelector('.sess', { state: 'attached', timeout: 5000 })
}

for (const [label, url] of [['the root page', '/'], ['a link to an unknown session', '/s/does-not-exist']]) {
  test(`${label}: the work head shows no session actions until a session is selected`, async ({ page }) => {
    const posts = recordActionPosts(page)
    await page.goto(url)
    await waitForMenu(page)

    const head = page.locator('.work-head')
    await expect(head).toBeVisible()
    await expect(head.getByRole('button', { name: ACTION_NAMES })).toHaveCount(0)
    // It must not name another session as if it were the current one.
    await expect(head).not.toContainText('agent-deck')
    await expect(page.getByTestId('work-head-empty')).toContainText('No session selected')
    // The right rail (desktop/tablet) must not describe an unselected session either.
    if ((page.viewportSize()?.width || 1280) > 720) {
      await expect(page.getByTestId('right-rail')).toContainText('no session selected')
    }
    expect(posts).toEqual([])
  })
}

test('after selecting a session the head acts on exactly that session', async ({ page }, testInfo) => {
  // At 820px (tablet project) the right rail overlaps the work head's
  // buttons, a separate layout bug; this test is about WHICH session the
  // head acts on, so the tablet project runs it at laptop width.
  if (testInfo.project.name === 'chromium-tablet') await page.setViewportSize({ width: 1280, height: 800 })
  const posts = recordActionPosts(page)
  await page.goto('/s/sess-002')
  await waitForMenu(page)

  const head = page.locator('.work-head')
  await expect(head.locator('.cur')).toHaveText('frontend')
  await head.getByRole('button', { name: 'Restart' }).click()
  await expect.poll(() => posts).toEqual(['/api/sessions/sess-002/restart'])
})
