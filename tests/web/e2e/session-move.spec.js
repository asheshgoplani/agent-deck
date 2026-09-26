// e2e/session-move.spec.js -- move a session to another group from the web UI
// (#2368). Closes the "Move session to group" MISSING row in
// tests/web/PARITY_MATRIX.md: the web side of the TUI's M and
// `agent-deck group move`, via POST /api/sessions/{id}/move.

import { test, expect } from '@playwright/test'

const SESSION_ID = 'sess-001'   // claude, fixture group "work"
const SHELL_ID = 'sess-004'     // shell, fixture group "personal"

async function resetFixture(request) {
  const res = await request.post('/__fixture/reset')
  expect(res.status()).toBe(204)
}

async function groupOf(request, id) {
  const list = await request.get('/api/sessions')
  expect(list.status()).toBe(200)
  const target = (await list.json()).sessions.find(s => s.id === id)
  expect(target).toBeTruthy()
  return target.groupPath
}

async function openEditDialog(page) {
  await page.goto('/')
  await page.waitForSelector('.sess', { timeout: 5000 })
  const firstSession = page.locator('.sess').first()
  await firstSession.click()
  await firstSession.locator('[data-testid="edit-session-btn"]').click()
  await page.waitForSelector('[data-testid="edit-session-dialog"]', { timeout: 5000 })
}

test.describe.configure({ mode: 'serial' })

test.describe('Move session — REST API', () => {
  test.beforeEach(async ({ request }) => {
    await resetFixture(request)
  })

  test('POST /api/sessions/:id/move moves the session (case-insensitive match)', async ({ request }) => {
    const res = await request.post(`/api/sessions/${SHELL_ID}/move`, { data: { groupPath: 'WORK/Innotrade' } })
    expect(res.status()).toBe(200)
    const body = await res.json()
    expect(body.sessionId).toBe(SHELL_ID)
    expect(body.groupPath).toBe('work/innotrade')
    expect(body.restartRequired).toBe(false)
    expect(await groupOf(request, SHELL_ID)).toBe('work/innotrade')
  })

  test('"root" moves the session to the default group', async ({ request }) => {
    const res = await request.post(`/api/sessions/${SHELL_ID}/move`, { data: { groupPath: 'root' } })
    expect(res.status()).toBe(200)
    expect((await res.json()).groupPath).toBe('my-sessions')
    expect(await groupOf(request, SHELL_ID)).toBe('my-sessions')
  })

  test('a claude session moving into a group with its own config dir reports restartRequired', async ({ request }) => {
    const res = await request.post(`/api/sessions/${SESSION_ID}/move`, { data: { groupPath: 'personal' } })
    expect(res.status()).toBe(200)
    expect((await res.json()).restartRequired).toBe(true)
  })

  test('unknown session returns 404', async ({ request }) => {
    const res = await request.post('/api/sessions/does-not-exist/move', { data: { groupPath: 'work' } })
    expect(res.status()).toBe(404)
  })
})

test.describe('Move session — Edit dialog', () => {
  test.beforeEach(async ({ request }, testInfo) => {
    // Same phone-viewport gap as edit-session.spec.js: the sidebar is
    // collapsed below 720px, so .sess rows are not reachable there.
    test.skip(
      testInfo.project.name === 'chromium-phone',
      'follow-up: sidebar collapsed on phone viewport; same gap as edit-session UI tests',
    )
    await resetFixture(request)
  })

  test('the GROUP select is seeded with the current group and lists every group', async ({ page }) => {
    await openEditDialog(page)
    const select = page.locator('[data-testid="edit-session-group"]')
    await expect(select).toHaveValue('work')
    const values = await select.locator('option').evaluateAll(opts => opts.map(o => o.value))
    expect(values).toEqual(expect.arrayContaining(['my-sessions', 'work', 'work/innotrade', 'personal']))
  })

  test('choosing a group and saving moves the session', async ({ page, request }) => {
    await openEditDialog(page)
    await page.locator('[data-testid="edit-session-group"]').selectOption('work/innotrade')
    await page.locator('[data-testid="edit-session-save"]').click()
    await expect(page.locator('[data-testid="edit-session-dialog"]')).toHaveCount(0, { timeout: 4000 })
    expect(await groupOf(request, SESSION_ID)).toBe('work/innotrade')
    // No config dir change, so no restart warning.
    await expect(page.locator('.toast', { hasText: 'Restart it' })).toHaveCount(0)
  })

  test('a move that changes the Claude config dir warns that a restart is needed', async ({ page, request }) => {
    await openEditDialog(page)
    await page.locator('[data-testid="edit-session-group"]').selectOption('personal')
    await page.locator('[data-testid="edit-session-save"]').click()
    await expect(page.locator('[data-testid="edit-session-dialog"]')).toHaveCount(0, { timeout: 4000 })
    expect(await groupOf(request, SESSION_ID)).toBe('personal')
    await expect(page.locator('.toast', { hasText: 'Restart it' })).toBeVisible({ timeout: 4000 })
  })

  test('a title edit alone does not move the session', async ({ page, request }) => {
    await openEditDialog(page)
    await page.locator('[data-testid="edit-session-title"]').fill('renamed-no-move')
    await page.locator('[data-testid="edit-session-save"]').click()
    await expect(page.locator('[data-testid="edit-session-dialog"]')).toHaveCount(0, { timeout: 4000 })
    expect(await groupOf(request, SESSION_ID)).toBe('work')
  })
})
