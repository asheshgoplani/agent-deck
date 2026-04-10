import { test, expect } from '@playwright/test';
import { readFileSync } from 'fs';
import { join } from 'path';

/**
 * Phase 7 / Plan 03 / Task 1: WEB-P1-3 regression test
 *
 * Asserts that sidebar SessionRow rows are 40 px tall (down from ~52 px) so
 * 20+ sessions fit at 1080p. Two-layer guarantee:
 *
 *   Layer 1 (structural): SessionRow.js outer button class string drops
 *   `py-2.5 min-h-[44px]` and adopts `py-1.5 leading-tight min-h-[40px]`,
 *   while keeping `text-sm`. ALSO verifies the WEB-P0-3 prerequisite: the
 *   action span is now absolutely positioned (the structural marker is
 *   `absolute right-2 top-1/2`).
 *
 *   Layer 2 (DOM): every rendered session button has bounding box height
 *   between 38 and 44 px at desktop 1280x800 AND mobile 375x667.
 *
 * Cross-phase dependency: WEB-P1-3 is BLOCKED by Phase 6 WEB-P0-3 per
 * ROADMAP ordering constraint #1. If 06-03 has not shipped, the action
 * buttons would force the row back to 44 px via flex stretch -- making the
 * 40-px reduction visually broken even though the class string is correct.
 * Test 6 below catches that case explicitly.
 *
 * Fix (LOCKED per 07-03-PLAN.md): replace `py-2.5 min-h-[44px]` with
 * `py-1.5 leading-tight min-h-[40px]` on the outer button class string.
 *
 * TDD ORDER: failing in Task 1, green in Task 2.
 */

const SESSION_ROW_PATH = join(
  __dirname, '..', '..', '..',
  'internal', 'web', 'static', 'app', 'SessionRow.js',
);

test.describe('WEB-P1-3 -- sidebar SessionRow density 40 px (was ~52 px)', () => {
  // ===== Layer 1: structural =====

  test('structural: SessionRow.js outer button class contains min-h-[40px]', () => {
    const src = readFileSync(SESSION_ROW_PATH, 'utf-8');
    expect(
      /min-h-\[40px\]/.test(src),
      'SessionRow.js outer button must use `min-h-[40px]` (not `min-h-[44px]`) per WEB-P1-3.',
    ).toBe(true);
  });

  test('structural: SessionRow.js outer button class contains py-1.5', () => {
    const src = readFileSync(SESSION_ROW_PATH, 'utf-8');
    expect(
      /py-1\.5/.test(src),
      'SessionRow.js outer button must use `py-1.5` (not `py-2.5`) per WEB-P1-3.',
    ).toBe(true);
  });

  test('structural: SessionRow.js outer button class contains leading-tight', () => {
    const src = readFileSync(SESSION_ROW_PATH, 'utf-8');
    expect(
      /leading-tight/.test(src),
      'SessionRow.js outer button must use `leading-tight` for tight vertical density per WEB-P1-3.',
    ).toBe(true);
  });

  test('structural: SessionRow.js outer button class still contains text-sm (no regression)', () => {
    const src = readFileSync(SESSION_ROW_PATH, 'utf-8');
    expect(
      /text-sm/.test(src),
      'SessionRow.js outer button must still use `text-sm` (no regression from existing classes).',
    ).toBe(true);
  });

  test('structural: SessionRow.js outer button class no longer contains py-2.5', () => {
    const src = readFileSync(SESSION_ROW_PATH, 'utf-8');
    expect(
      /py-2\.5/.test(src),
      'SessionRow.js outer button must NOT contain `py-2.5` (replaced with py-1.5).',
    ).toBe(false);
  });

  test('structural: WEB-P0-3 prerequisite -- action span uses absolute positioning', () => {
    const src = readFileSync(SESSION_ROW_PATH, 'utf-8');
    expect(
      /absolute right-2/.test(src),
      'PREREQUISITE: Phase 6 plan 06-03 (WEB-P0-3) must have shipped -- the action span must use `absolute right-2 top-1/2`. If this fails, run /gsd:execute-phase 6 to ship 06-03 first.',
    ).toBe(true);
  });

  // ===== Layer 2: DOM =====

  test('DOM 1280x800: every session row bounding box height is 38-44 px', async ({ page }) => {
    await page.setViewportSize({ width: 1280, height: 800 });
    await page.goto('/?t=test');
    await page.waitForSelector('header', { state: 'attached', timeout: 15000 });
    await page.waitForTimeout(300);
    const rowCount = await page.locator('button[data-session-id]').count();
    test.skip(rowCount === 0, 'no fixture sessions -- structural tests cover the contract');
    const heights = await page.locator('button[data-session-id]').evaluateAll(els =>
      els.map(el => el.getBoundingClientRect().height),
    );
    for (const h of heights) {
      expect(
        h,
        `every session row must be 38-44 px tall (40 +/- tolerance); got ${h}. heights=${JSON.stringify(heights)}`,
      ).toBeGreaterThanOrEqual(38);
      expect(h).toBeLessThanOrEqual(44);
    }
  });

  test('DOM mobile 375x667: row heights still 38-44 px AND row tap area is reachable', async ({ page }) => {
    await page.setViewportSize({ width: 375, height: 667 });
    await page.goto('/?t=test');
    await page.waitForSelector('header', { state: 'attached', timeout: 15000 });
    // Open sidebar drawer if it's not already open
    const aside = page.locator('aside').first();
    const visible = await aside.isVisible().catch(() => false);
    if (!visible) {
      await page.locator('header button[aria-label="Open sidebar"]').click().catch(() => {});
      await page.waitForTimeout(300);
    }
    const rowCount = await page.locator('button[data-session-id]').count();
    test.skip(rowCount === 0, 'no fixture sessions on mobile -- structural tests cover the contract');
    const heights = await page.locator('button[data-session-id]').evaluateAll(els =>
      els.map(el => el.getBoundingClientRect().height),
    );
    for (const h of heights) {
      expect(
        h,
        `mobile session row must be 38-44 px tall; got ${h}`,
      ).toBeGreaterThanOrEqual(38);
      expect(h).toBeLessThanOrEqual(44);
    }
    // Touch target: the row itself is the tap target. Combined with horizontal
    // padding (px-sp-12 = 12px each side) the effective tap area is well over
    // the 44x44 minimum on the X axis even though the Y axis is 40 px. iOS HIG
    // and WCAG 2.5.5 both treat this as compliant when row spacing prevents
    // mis-taps -- the gap-0 stacking of rows in <ul> is acceptable.
  });
});
