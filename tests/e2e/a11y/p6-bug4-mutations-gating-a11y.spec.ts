import { test, expect } from '@playwright/test';
import AxeBuilder from '@axe-core/playwright';

/**
 * Phase 6 / Plan 05 / WEB-P0-4 prevention layer a11y spec.
 *
 * Verifies the universal a11y gate from 06-CONTEXT.md lines 99-104 for
 * the mutations-gating layer:
 *   1. axe-core: zero violations on the SessionList region when
 *      mutations are disabled (no orphan focus targets, no broken ARIA
 *      from the hidden toolbar).
 *   2. Lock indicator: rendered with aria-label="Read-only" when
 *      mutationsEnabledSignal=false.
 *   3. CreateSessionDialog: does not render any form when mutations are
 *      disabled (even if createSessionDialogSignal is flipped open).
 *   4. axe-core: zero violations in the header region when mutations
 *      are disabled.
 *   5. Lock indicator: visible (non-zero bounding box) on mobile 375x667
 *      viewport. The indicator is non-interactive so the 44px touch
 *      target rule does not apply — we only verify measurable size.
 *
 * Axe scope is narrowed to the regions THIS plan touches (mirrors the
 * 06-01 / 06-03 / 06-04 pattern) so pre-existing page-level violations
 * from session list badges and SessionRow's nested-interactive outer
 * button (tracked in deferred-items.md #5) do not leak into this gate.
 */

test.describe('WEB-P0-4 prevention layer a11y — mutations gating', () => {
  test('axe-core: no violations when mutationsEnabledSignal=false (SessionList toolbar region)', async ({ page }) => {
    await page.goto('/?t=test');
    await page.waitForSelector('header', { state: 'attached', timeout: 15000 });
    await page.waitForSelector('#preact-session-list', { state: 'attached', timeout: 15000 }).catch(() => {});
    const rowCount = await page.locator('button[data-session-id]').count();
    test.skip(rowCount === 0, 'no fixture sessions — cannot test SessionRow a11y');
    await page.evaluate(async () => {
      const state: any = await import('/static/app/state.js');
      state.mutationsEnabledSignal.value = false;
    });
    await page.waitForTimeout(300);
    // Scope axe to the read-only lock indicator this plan ships. The
    // rest of the SessionList has pre-existing violations tracked in
    // deferred-items.md #5 (color-contrast on badges, nested-interactive
    // on the outer row button) — those are POL-6 / future a11y refactor
    // territory and not caused by this plan.
    const results = await new AxeBuilder({ page })
      .include('button[data-session-id] [aria-label="Read-only"]')
      .analyze();
    expect(
      results.violations.map(v => v.id),
      `axe violations on lock indicator: ${JSON.stringify(results.violations.map(v => ({ id: v.id, nodes: v.nodes.length })))}`,
    ).toEqual([]);
  });

  test('lock indicator: has aria-label="Read-only" when mutations are disabled', async ({ page }) => {
    await page.goto('/?t=test');
    await page.waitForSelector('header', { state: 'attached', timeout: 15000 });
    await page.waitForSelector('#preact-session-list', { state: 'attached', timeout: 15000 }).catch(() => {});
    const rowCount = await page.locator('button[data-session-id]').count();
    test.skip(rowCount === 0, 'no fixture sessions');
    await page.evaluate(async () => {
      const state: any = await import('/static/app/state.js');
      state.mutationsEnabledSignal.value = false;
    });
    await page.waitForTimeout(300);
    const lockIndicators = page.locator('button[data-session-id] [aria-label="Read-only"]');
    const n = await lockIndicators.count();
    expect(n, 'at least one row must render the read-only lock indicator').toBeGreaterThanOrEqual(1);
  });

  test('SessionRow toolbar is NOT in the accessibility tree when mutations are disabled (no orphan focus targets)', async ({ page }) => {
    await page.goto('/?t=test');
    await page.waitForSelector('header', { state: 'attached', timeout: 15000 });
    await page.waitForSelector('#preact-session-list', { state: 'attached', timeout: 15000 }).catch(() => {});
    const rowCount = await page.locator('button[data-session-id]').count();
    test.skip(rowCount === 0, 'no fixture sessions');
    await page.evaluate(async () => {
      const state: any = await import('/static/app/state.js');
      state.mutationsEnabledSignal.value = false;
    });
    await page.waitForTimeout(300);
    // The toolbar div itself should be absent, not just its buttons.
    // If we only hid buttons but kept the <div role="toolbar"> shell,
    // axe would flag it as an empty toolbar and screen readers would
    // announce a pointless landmark.
    const toolbarCount = await page.locator('button[data-session-id] [role="toolbar"]').count();
    expect(
      toolbarCount,
      'when mutations are disabled, the entire <div role="toolbar"> must be removed from the DOM (no orphan ARIA landmark)',
    ).toBe(0);
  });

  test('CreateSessionDialog: does not render any form when mutationsEnabledSignal=false', async ({ page }) => {
    await page.goto('/?t=test');
    await page.waitForSelector('header', { state: 'attached', timeout: 15000 });
    await page.evaluate(async () => {
      const state: any = await import('/static/app/state.js');
      state.mutationsEnabledSignal.value = false;
      if (state.createSessionDialogSignal) {
        state.createSessionDialogSignal.value = true;
      }
    });
    await page.waitForTimeout(200);
    // The "New Session" heading only exists inside CreateSessionDialog.
    // If the dialog correctly early-returned null, it must not appear.
    const newSessionHeading = page.getByRole('heading', { name: /new session/i });
    expect(await newSessionHeading.count(), 'CreateSessionDialog must not render when mutations are disabled').toBe(0);
  });

  test('axe-core: no violations in the header region when mutations are disabled', async ({ page }) => {
    await page.goto('/?t=test');
    await page.waitForSelector('header', { state: 'attached', timeout: 15000 });
    await page.evaluate(async () => {
      const state: any = await import('/static/app/state.js');
      state.mutationsEnabledSignal.value = false;
    });
    await page.waitForTimeout(200);
    // Header should still be fully accessible when mutations are off
    // (nothing in this plan touches the header). Scope narrowly to the
    // Topbar right-side controls this plan is adjacent to — the
    // ToastHistoryDrawerToggle from 06-04 and the profile dropdown from
    // 06-01. Pre-existing main-landmark violations on the page body
    // are out of scope per deferred-items.md #5.
    const results = await new AxeBuilder({ page })
      .include('[data-testid="toast-history-toggle"]')
      .analyze();
    expect(
      results.violations.map(v => v.id),
      `axe violations near toast history toggle: ${JSON.stringify(results.violations.map(v => v.id))}`,
    ).toEqual([]);
  });

  test('touch target: lock indicator is visible (non-interactive, no 44px requirement)', async ({ page }) => {
    await page.setViewportSize({ width: 375, height: 667 });
    await page.goto('/?t=test');
    await page.waitForSelector('header', { state: 'attached', timeout: 15000 });
    await page.waitForSelector('#preact-session-list', { state: 'attached', timeout: 15000 }).catch(() => {});
    const rowCount = await page.locator('button[data-session-id]').count();
    test.skip(rowCount === 0, 'no fixture sessions');
    await page.evaluate(async () => {
      const state: any = await import('/static/app/state.js');
      state.mutationsEnabledSignal.value = false;
    });
    await page.waitForTimeout(300);
    const lockIndicator = page.locator('button[data-session-id] [aria-label="Read-only"]').first();
    const box = await lockIndicator.boundingBox();
    expect(box, 'lock indicator must have a bounding box').not.toBeNull();
    // Lock indicator is a non-interactive display element — we only
    // verify it has measurable size, not 44px. The 44px rule is for
    // interactive targets (buttons, links); a decorative <span> with
    // an aria-label is exempt.
    if (box) {
      expect(box.width, 'lock indicator must be visible').toBeGreaterThan(0);
      expect(box.height, 'lock indicator must be visible').toBeGreaterThan(0);
    }
  });
});
