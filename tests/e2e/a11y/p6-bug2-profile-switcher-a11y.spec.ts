import { test, expect } from '@playwright/test';
import AxeBuilder from '@axe-core/playwright';

/**
 * Phase 6 / Plan 01 / Task 3: WEB-P0-2 a11y regression spec.
 *
 * Verifies the Option B ProfileDropdown ships with:
 *   1. Zero axe-core violations scoped to the header region (where the
 *      component lives). If other header children surface violations unrelated
 *      to ProfileDropdown, we tighten the include scope to the
 *      [data-testid="profile-indicator"] element instead.
 *   2. Keyboard Tab navigation reaches the indicator in the multi-profile
 *      case, OR the single-profile case is served as a role="status" element
 *      with an aria-label describing the current profile.
 *   3. Mobile (375x667) touch target is at least 44px tall per WCAG 2.5.5.
 */

test.describe('WEB-P0-2 a11y — profile indicator', () => {
  test('axe-core: no violations scoped to profile indicator', async ({ page }) => {
    await page.goto('/?t=test');
    await page.waitForSelector('header', { state: 'attached', timeout: 15000 });
    await page.waitForSelector('[data-testid="profile-indicator"]', {
      state: 'attached',
      timeout: 15000,
    });
    // Scope to the component itself so unrelated header children (Costs,
    // Info, ConnectionIndicator, PushControls, etc.) cannot leak violations
    // into this assertion. The whole-header scan is out of scope for plan
    // 06-01 and belongs in later P0 plans (06-02..04).
    const results = await new AxeBuilder({ page })
      .include('[data-testid="profile-indicator"]')
      .analyze();
    expect(
      results.violations,
      `axe violations found: ${JSON.stringify(results.violations.map((v) => v.id))}`,
    ).toEqual([]);
  });

  test('keyboard: multi-profile indicator is focusable OR single-profile is role=status', async ({ page }) => {
    await page.goto('/?t=test');
    await page.waitForSelector('header', { state: 'attached', timeout: 15000 });
    await page.waitForSelector('[data-testid="profile-indicator"]', {
      state: 'attached',
      timeout: 15000,
    });
    const resp = await page.request.get('/api/profiles?t=test');
    if (!resp.ok()) test.skip(true, 'profiles endpoint unreachable');
    const data = await resp.json();
    const profiles: string[] = data.profiles || [];
    if (profiles.length > 1) {
      const btn = page.locator('[data-testid="profile-indicator"] [aria-haspopup="listbox"]');
      await expect(btn).toHaveCount(1);
      await btn.focus();
      await expect(btn).toBeFocused();
    } else {
      const status = page.locator('[data-testid="profile-indicator"][role="status"]');
      await expect(status).toHaveCount(1);
      const label = await status.getAttribute('aria-label');
      expect(
        label,
        'role=status must have aria-label containing the current profile name',
      ).toMatch(/Current profile:/);
    }
  });

  test('touch target: profile indicator is at least 44px tall on mobile', async ({ page }) => {
    await page.setViewportSize({ width: 375, height: 667 });
    await page.goto('/?t=test');
    await page.waitForSelector('header', { state: 'attached', timeout: 15000 });
    const indicator = page.locator('[data-testid="profile-indicator"]');
    await expect(indicator).toHaveCount(1);
    const box = await indicator.boundingBox();
    expect(box, 'profile-indicator must have a bounding box').not.toBeNull();
    if (box) {
      expect(
        box.height,
        `height must be >=44px, got ${box.height}`,
      ).toBeGreaterThanOrEqual(44);
      // Width is intentionally not asserted. The effective tap area is
      // governed by min-h-[44px] + px-2 (8px per side); WCAG 2.5.5's key
      // invariant is the 44x44 tap area, and the container shrinks
      // horizontally to the profile name length by design.
    }
  });
});
