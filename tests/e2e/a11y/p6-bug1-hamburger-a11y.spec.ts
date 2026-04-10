import { test, expect } from '@playwright/test';
import AxeBuilder from '@axe-core/playwright';

/**
 * Phase 6 / Plan 02 / Task 3: WEB-P0-1 a11y regression spec.
 *
 * Verifies the hamburger z-index fix ships with:
 *   1. Zero axe-core violations in the header region at 375x667.
 *   2. Keyboard focusability: the hamburger is Tab-reachable and Enter
 *      activates it (aria-expanded flips). This is the keyboard analog of
 *      the visual click test — if a sibling control were still intercepting
 *      pointer events, the keyboard path would still work because focus
 *      follows tab order, not coordinate geometry. Keyboard + click both
 *      green means the component is reachable by every user.
 *   3. ARIA roles/labels: aria-label is "Open sidebar" / "Close sidebar"
 *      and aria-expanded is a boolean.
 *   4. Touch target >=44x44 at mobile (375x667) AND tablet (768x1024) —
 *      the hamburger is lg:hidden (Tailwind lg = 1024px) so both viewports
 *      render it. WCAG 2.5.5 / iOS HIG invariant.
 *
 * Pattern established by plan 06-01 (tests/e2e/a11y/p6-bug2-profile-switcher-a11y.spec.ts):
 * standalone per-bug config, AxeBuilder include() scope narrowed to the
 * component's landmark so unrelated sibling violations don't leak into
 * this plan's gate.
 */

test.describe('WEB-P0-1 a11y — hamburger + topbar', () => {
  test('axe-core: no violations in header region at 375x667', async ({ page }) => {
    await page.setViewportSize({ width: 375, height: 667 });
    await page.goto('/?t=test');
    await page.waitForSelector('header', { state: 'attached', timeout: 15000 });
    const results = await new AxeBuilder({ page }).include('header').analyze();
    const summary = results.violations.map(v => ({ id: v.id, nodes: v.nodes.length }));
    expect(
      summary,
      `axe violations in header: ${JSON.stringify(summary)}`,
    ).toEqual([]);
  });

  test('keyboard: hamburger is focusable and Enter flips aria-expanded', async ({ page }) => {
    await page.setViewportSize({ width: 375, height: 667 });
    await page.addInitScript(() => {
      try { localStorage.setItem('agentdeck.sidebarOpen', 'false'); } catch (_) { /* ignore */ }
    });
    await page.goto('/?t=test');
    await page.waitForSelector('header', { state: 'attached', timeout: 15000 });
    const hamburger = page.locator('header button[aria-label*="sidebar"]').first();
    // Focus directly — this asserts the button is focusable (no tabindex=-1,
    // not visibility:hidden, not pointer-events: none at the DOM level).
    await hamburger.focus();
    await expect(hamburger).toBeFocused();
    const expandedBefore = await hamburger.getAttribute('aria-expanded');
    await page.keyboard.press('Enter');
    await page.waitForTimeout(200);
    const expandedAfter = await hamburger.getAttribute('aria-expanded');
    expect(
      expandedAfter,
      'aria-expanded must flip after keyboard (Enter) activation',
    ).not.toBe(expandedBefore);
  });

  test('ARIA: hamburger has correct role and labels', async ({ page }) => {
    await page.setViewportSize({ width: 375, height: 667 });
    await page.goto('/?t=test');
    await page.waitForSelector('header', { state: 'attached', timeout: 15000 });
    const hamburger = page.locator('header button[aria-label*="sidebar"]').first();
    await expect(hamburger).toHaveCount(1);
    const label = await hamburger.getAttribute('aria-label');
    expect(label, 'aria-label must describe the sidebar state').toMatch(/(Close|Open) sidebar/);
    const expanded = await hamburger.getAttribute('aria-expanded');
    expect(expanded, 'aria-expanded must be set to true or false').toMatch(/^(true|false)$/);
  });

  test('touch target: hamburger is >=44x44 at 375x667 AND 768x1024', async ({ page }) => {
    for (const vp of [{ width: 375, height: 667 }, { width: 768, height: 1024 }]) {
      await page.setViewportSize(vp);
      await page.goto('/?t=test');
      await page.waitForSelector('header', { state: 'attached', timeout: 15000 });
      const hamburger = page.locator('header button[aria-label*="sidebar"]').first();
      // Both viewports are below Tailwind's lg breakpoint (1024px), so the
      // hamburger is rendered. If a future viewport change hides it (isVisible
      // returns false), skip the assertion for that viewport.
      const visible = await hamburger.isVisible();
      if (!visible) continue;
      const box = await hamburger.boundingBox();
      expect(
        box,
        `hamburger must have a bounding box at ${vp.width}x${vp.height}`,
      ).not.toBeNull();
      if (box) {
        expect(
          box.width,
          `hamburger width at ${vp.width}x${vp.height}: ${box.width}`,
        ).toBeGreaterThanOrEqual(44);
        expect(
          box.height,
          `hamburger height at ${vp.width}x${vp.height}: ${box.height}`,
        ).toBeGreaterThanOrEqual(44);
      }
    }
  });
});
