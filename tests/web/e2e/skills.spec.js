// e2e/skills.spec.js -- Skills tab end-to-end coverage.
//
// Verifies the four endpoints + UI added in feat(web): Skills management
// (closes the MISSING rows under "SKILLS MANAGEMENT" in PARITY_MATRIX.md).
//
// Per ~/.agent-deck/skills/pool/agent-deck-tdd-feature/SKILL.md we cover
// happy + failure + boundary cases against the live fixture web server.

import { test, expect } from '@playwright/test'

async function gotoSkills(page) {
  await page.goto('/')
  await page.waitForSelector('.sess', { timeout: 5000 })
  // Click the first session row (sess-001 / agent-deck — has alpha attached
  // in the fixture seed).
  await page.locator('.sess').first().click()
  // Activate the Skills tab in the top navigation.
  await page.getByRole('button', { name: /^skills$/i }).click()
  await page.waitForSelector('[data-testid="skills-pane"]', { timeout: 5000 })
}

test.describe('skills management', () => {
  test.beforeEach(async ({ request }) => {
    await request.post('/__fixture/reset')
  })

  test('GET /api/skills returns the catalog', async ({ request }) => {
    const res = await request.get('/api/skills')
    expect(res.status()).toBe(200)
    const body = await res.json()
    expect(Array.isArray(body.skills)).toBe(true)
    expect(body.skills.length).toBeGreaterThanOrEqual(3)
    const names = body.skills.map(s => s.name)
    expect(names).toContain('alpha')
    expect(names).toContain('beta')
  })

  test('GET /api/sessions/{id}/skills returns the seeded attachment', async ({ request }) => {
    const res = await request.get('/api/sessions/sess-001/skills')
    expect(res.status()).toBe(200)
    const body = await res.json()
    expect(body.skills.map(s => s.name)).toEqual(['alpha'])
  })

  test('GET on a missing session returns 404', async ({ request }) => {
    const res = await request.get('/api/sessions/does-not-exist/skills')
    expect(res.status()).toBe(404)
  })

  test('POST attaches a skill, DELETE detaches it', async ({ request }) => {
    // Attach beta.
    const attach = await request.post('/api/sessions/sess-002/skills/beta?source=pool')
    expect(attach.status()).toBe(200)
    const attBody = await attach.json()
    expect(attBody.skill.name).toBe('beta')

    // Confirm via GET.
    const list = await request.get('/api/sessions/sess-002/skills')
    expect(list.status()).toBe(200)
    const listBody = await list.json()
    expect(listBody.skills.map(s => s.name)).toContain('beta')

    // Detach.
    const detach = await request.delete('/api/sessions/sess-002/skills/beta?source=pool')
    expect(detach.status()).toBe(200)

    // Confirm gone.
    const after = await request.get('/api/sessions/sess-002/skills')
    const afterBody = await after.json()
    expect(afterBody.skills.map(s => s.name)).not.toContain('beta')
  })

  test('POST on a session whose tool does not support skills returns 400', async ({ request }) => {
    // sess-004 has tool="shell"; the catalog system rejects this.
    const res = await request.post('/api/sessions/sess-004/skills/alpha?source=pool')
    expect(res.status()).toBe(400)
  })

  test('UI: Skills tab lists the seeded attachment and catalog', async ({ page, viewport }) => {
    // desktop-only: gotoSkills() clicks sidebar `.sess` rows + the desktop Skills tab nav, absent on the phone touch-first layout (<768px). The API tests above stay phone-applicable.
    test.skip((viewport?.width || 1280) < 768, 'phone viewport: skills UI is desktop/tablet only')
    await gotoSkills(page)
    // The attached column should show alpha.
    const attachedRows = page.locator('[data-testid="skill-attached-row"]')
    await expect(attachedRows).toHaveCount(1)
    await expect(attachedRows.first()).toContainText('alpha')

    // Catalog should include beta and gamma (alpha is hidden because it's already attached).
    const catalogRows = page.locator('[data-testid="skill-catalog-row"]')
    await expect(catalogRows).toHaveCount(2)
    await expect(catalogRows.first()).toContainText(/beta|gamma/)
  })

  test('UI: clicking Attach moves a skill from catalog to attached', async ({ page, viewport }) => {
    // desktop-only: gotoSkills() clicks sidebar `.sess` rows + the desktop Skills tab nav, absent on the phone touch-first layout (<768px). The API tests above stay phone-applicable.
    test.skip((viewport?.width || 1280) < 768, 'phone viewport: skills UI is desktop/tablet only')
    await gotoSkills(page)
    // Click the first available catalog row's Attach button.
    const firstCatalog = page.locator('[data-testid="skill-catalog-row"]').first()
    const skillName = (await firstCatalog.locator('strong').first().textContent()) || ''
    await firstCatalog.locator('[data-testid="skill-attach-btn"]').click()
    // The attached list should now contain this skill name.
    await expect(page.locator('[data-testid="skill-attached-row"]', { hasText: skillName.trim() })).toBeVisible({ timeout: 4000 })
  })

  test('UI: clicking Detach removes a skill from the attached list', async ({ page, viewport }) => {
    // desktop-only: gotoSkills() clicks sidebar `.sess` rows + the desktop Skills tab nav, absent on the phone touch-first layout (<768px). The API tests above stay phone-applicable.
    test.skip((viewport?.width || 1280) < 768, 'phone viewport: skills UI is desktop/tablet only')
    await gotoSkills(page)
    const row = page.locator('[data-testid="skill-attached-row"]').first()
    await row.locator('[data-testid="skill-detach-btn"]').click()
    // Now empty state should be visible.
    await expect(page.locator('[data-testid="skills-attached-empty"]')).toBeVisible({ timeout: 4000 })
  })
})
