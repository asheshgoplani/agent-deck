import { test, expect } from '@playwright/test';
import AxeBuilder from '@axe-core/playwright';

/**
 * Phase 6 / Plan 03 / Task 3: WEB-P0-3 a11y regression spec.
 *
 * Verifies the absolute-positioned action toolbar ships with:
 *   1. Zero axe-core violations scoped to the toolbar element itself
 *      (`[role="toolbar"][aria-label="Session actions"]`). The wider
 *      session-list scan surfaces two PRE-EXISTING violations that are
 *      out of scope for this plan — see
 *      `.planning/phases/06-critical-p0-bugs/deferred-items.md`:
 *        - `color-contrast` (2 nodes): .dark:text-tn-muted/60 / .text-gray-400
 *          on white backgrounds at 2.55-2.6:1 (needs 4.5:1)
 *        - `nested-interactive` (1 node): the outer <button data-session-id>
 *          contains focusable inner <button> action children. This is
 *          structural to the session row since day one — pre-dates plan 06-03.
 *      Narrowing the scope to the toolbar node is the 06-01 pattern (see
 *      `tests/e2e/a11y/p6-bug2-profile-switcher-a11y.spec.ts` axe scope).
 *   2. role="toolbar" + aria-label="Session actions" are present on the
 *      rendered DOM (not just in source).
 *   3. Keyboard focus on an action button reveals the toolbar via the
 *      Preact `hasFocusWithin` state on the outer button (the JSX state
 *      fallback Tailwind's `group-focus-within:*` — see Rule 1 auto-fix
 *      in the SUMMARY).
 *   4. Each action button has a bounding box of at least 44x44 px on
 *      desktop (WCAG 2.5.5 / iOS HIG touch target invariant).
 */

test.describe('WEB-P0-3 a11y — session action toolbar', () => {
  test('axe-core: no violations scoped to the action toolbar', async ({ page }) => {
    await page.goto('/?t=test');
    await page.waitForSelector('header', { state: 'attached', timeout: 15000 });
    await page
      .waitForSelector('#preact-session-list', { state: 'attached', timeout: 15000 })
      .catch(() => {});
    // WEB-P0-4 prevention layer (06-05): the toolbar is gated on
    // mutationsEnabledSignal, which reads /api/settings.webMutations on
    // AppShell mount. The test server may run with webMutations=false,
    // in which case the toolbar correctly does not render. This spec
    // tests the 06-03 toolbar a11y contract, so force the signal on.
    await page.evaluate(async () => {
      const state: any = await import('/static/app/state.js');
      state.mutationsEnabledSignal.value = true;
    });
    await page.waitForTimeout(200);
    const rowCount = await page.locator('button[data-session-id]').count();
    test.skip(rowCount === 0, 'no fixture sessions — axe needs a rendered toolbar');
    // Hover the first row so the toolbar is opacity:1 (axe ignores
    // elements hidden by opacity:0 for color-contrast checks but we want
    // deterministic scoping to a visible node).
    await page.locator('button[data-session-id]').first().hover();
    await page.waitForTimeout(200);
    // Narrow the scope to the toolbar element per the 06-01 pattern.
    // Pre-existing violations in sibling elements (color-contrast on
    // badges, nested-interactive on the outer button tree) are logged
    // in deferred-items.md and belong to future a11y-cleanup plans.
    const results = await new AxeBuilder({ page })
      .include('[role="toolbar"][aria-label="Session actions"]')
      .analyze();
    expect(
      results.violations.map((v) => v.id),
      `axe violations on toolbar: ${JSON.stringify(
        results.violations.map((v) => ({ id: v.id, nodes: v.nodes.length })),
      )}`,
    ).toEqual([]);
  });

  test('ARIA: toolbar is present with correct role and label', async ({ page }) => {
    await page.goto('/?t=test');
    await page.waitForSelector('header', { state: 'attached', timeout: 15000 });
    await page
      .waitForSelector('#preact-session-list', { state: 'attached', timeout: 15000 })
      .catch(() => {});
    // WEB-P0-4 prevention layer (06-05): the toolbar is gated on
    // mutationsEnabledSignal, which reads /api/settings.webMutations on
    // AppShell mount. The test server may run with webMutations=false,
    // in which case the toolbar correctly does not render. This spec
    // tests the 06-03 toolbar a11y contract, so force the signal on.
    await page.evaluate(async () => {
      const state: any = await import('/static/app/state.js');
      state.mutationsEnabledSignal.value = true;
    });
    await page.waitForTimeout(200);
    const rowCount = await page.locator('button[data-session-id]').count();
    test.skip(rowCount === 0, 'no fixture sessions');
    const toolbar = page.locator('[role="toolbar"][aria-label="Session actions"]').first();
    await expect(toolbar).toHaveCount(1);
  });

  test('keyboard: focusing an action button reveals the toolbar', async ({ page }) => {
    await page.goto('/?t=test');
    await page.waitForSelector('header', { state: 'attached', timeout: 15000 });
    await page
      .waitForSelector('#preact-session-list', { state: 'attached', timeout: 15000 })
      .catch(() => {});
    // WEB-P0-4 prevention layer (06-05): the toolbar is gated on
    // mutationsEnabledSignal, which reads /api/settings.webMutations on
    // AppShell mount. The test server may run with webMutations=false,
    // in which case the toolbar correctly does not render. This spec
    // tests the 06-03 toolbar a11y contract, so force the signal on.
    await page.evaluate(async () => {
      const state: any = await import('/static/app/state.js');
      state.mutationsEnabledSignal.value = true;
    });
    await page.waitForTimeout(200);
    const rowCount = await page.locator('button[data-session-id]').count();
    test.skip(rowCount === 0, 'no fixture sessions');

    // Focus the first action button inside the first row via Playwright's
    // focus() — this fires a real focus event that bubbles to the outer
    // button, triggering the onFocus handler which sets `hasFocusWithin`.
    const toolbarBtn = page.locator('button[data-session-id] [role="toolbar"] button').first();
    await toolbarBtn.focus();
    // Give Preact time to flush the state update and re-render.
    await page.waitForTimeout(200);
    // Also wait for the 120ms opacity transition to complete.
    const opacity = await page.evaluate(() => {
      const tb = document.querySelector(
        'button[data-session-id] [role="toolbar"]',
      ) as HTMLElement | null;
      if (!tb) return null;
      return getComputedStyle(tb).opacity;
    });
    expect(
      opacity,
      'focusing an action button via keyboard must reveal the toolbar (opacity 1 via hasFocusWithin Preact state)',
    ).toBe('1');
  });

  test('touch target: each action button is at least 44x44 after hovering the row', async ({
    page,
  }) => {
    await page.goto('/?t=test');
    await page.waitForSelector('header', { state: 'attached', timeout: 15000 });
    await page
      .waitForSelector('#preact-session-list', { state: 'attached', timeout: 15000 })
      .catch(() => {});
    // WEB-P0-4 prevention layer (06-05): the toolbar is gated on
    // mutationsEnabledSignal, which reads /api/settings.webMutations on
    // AppShell mount. The test server may run with webMutations=false,
    // in which case the toolbar correctly does not render. This spec
    // tests the 06-03 toolbar a11y contract, so force the signal on.
    await page.evaluate(async () => {
      const state: any = await import('/static/app/state.js');
      state.mutationsEnabledSignal.value = true;
    });
    await page.waitForTimeout(200);
    const rowCount = await page.locator('button[data-session-id]').count();
    test.skip(rowCount === 0, 'no fixture sessions');
    const row = page.locator('button[data-session-id]').first();
    await row.hover();
    await page.waitForTimeout(200);
    const buttons = row.locator('[role="toolbar"] button');
    const n = await buttons.count();
    expect(n, 'at least the delete button must exist in the toolbar').toBeGreaterThanOrEqual(1);
    for (let i = 0; i < n; i++) {
      const box = await buttons.nth(i).boundingBox();
      expect(box, `toolbar button ${i} must have a bounding box`).not.toBeNull();
      if (box) {
        expect(
          box.width,
          `button ${i} width must be >=44, got ${box.width}`,
        ).toBeGreaterThanOrEqual(44);
        expect(
          box.height,
          `button ${i} height must be >=44, got ${box.height}`,
        ).toBeGreaterThanOrEqual(44);
      }
    }
  });
});
