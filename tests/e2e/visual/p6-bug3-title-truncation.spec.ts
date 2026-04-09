import { test, expect } from '@playwright/test';
import { readFileSync } from 'fs';
import { join } from 'path';

/**
 * Phase 6 / Plan 03 / Task 1: WEB-P0-3 regression spec.
 *
 * Root cause (06-CONTEXT.md lines 70-83): SessionRow.js action button
 * container is a flex child with 4 buttons of `min-w-[44px]` plus `gap-0.5`
 * and `ml-1`, reserving `4*44 + 3*2 + 4 = 186px` of horizontal row space
 * even when the buttons are hidden via `opacity-0 pointer-events-none`.
 * The `flex-1 truncate min-w-0` title span therefore only receives
 * `row-width - 186px - dot - tool-badge - cost-badge` and truncates at
 * ~35% of the row width.
 *
 * Fix (locked): convert the action button container to
 * `position: absolute right-2 top-1/2 -translate-y-1/2`, wrap it in
 * `<div role="toolbar" aria-label="Session actions">`, reveal on
 * hover/focus-visible/isSelected with a 120ms opacity transition
 * respecting `prefers-reduced-motion`, and add
 * `focus-visible:opacity-100 focus-visible:pointer-events-auto` to each
 * action button so keyboard Tab traversal reveals the toolbar via
 * `group-focus-within` on the outer button `group`.
 *
 * TDD ORDER: this spec is committed in FAILING state in Task 1, then the
 * fix lands in Task 2, flipping the spec to green.
 *
 * STRUCTURAL FALLBACK: the readFileSync tests always run regardless of
 * fixture session availability. They provide the failing-before-fix TDD
 * guarantee even when no sessions render.
 */

const SESSION_ROW_PATH = join(
  __dirname,
  '..',
  '..',
  '..',
  'internal',
  'web',
  'static',
  'app',
  'SessionRow.js',
);

test.describe('WEB-P0-3 — absolute-positioned action toolbar', () => {
  test('structural: SessionRow.js contains role="toolbar"', () => {
    const src = readFileSync(SESSION_ROW_PATH, 'utf-8');
    expect(
      /role="toolbar"/.test(src),
      'SessionRow.js action button group must be wrapped in role="toolbar" per 06-CONTEXT.md line 78.',
    ).toBe(true);
  });

  test('structural: SessionRow.js contains absolute right-2 positioning', () => {
    const src = readFileSync(SESSION_ROW_PATH, 'utf-8');
    expect(
      /absolute right-2/.test(src),
      'SessionRow.js action toolbar must use `absolute right-2` per 06-CONTEXT.md line 74.',
    ).toBe(true);
  });

  test('structural: SessionRow.js contains vertical center translate', () => {
    const src = readFileSync(SESSION_ROW_PATH, 'utf-8');
    expect(
      /-translate-y-1\/2/.test(src),
      'SessionRow.js action toolbar must use `-translate-y-1/2` for vertical centering.',
    ).toBe(true);
  });

  test('structural: SessionRow.js outer button has relative positioning', () => {
    const src = readFileSync(SESSION_ROW_PATH, 'utf-8');
    // The outer button class currently contains `group w-full min-w-0 flex items-center`.
    // After fix, it must also contain `relative` so the absolute toolbar can anchor to it.
    const outerButtonRe =
      /group w-full\s+min-w-0\s+relative\s+flex items-center|group w-full\s+relative\s+min-w-0\s+flex items-center|group\s+relative\s+w-full\s+min-w-0\s+flex items-center|relative\s+group\s+w-full\s+min-w-0\s+flex items-center/;
    expect(
      outerButtonRe.test(src),
      'SessionRow.js outer button class must contain `relative` to establish a positioning context for the absolute toolbar.',
    ).toBe(true);
  });

  test('structural: SessionRow.js respects prefers-reduced-motion', () => {
    const src = readFileSync(SESSION_ROW_PATH, 'utf-8');
    expect(
      /motion-reduce:transition-none/.test(src),
      'SessionRow.js must honor prefers-reduced-motion via `motion-reduce:transition-none` per 06-CONTEXT.md line 81.',
    ).toBe(true);
  });

  test('structural: SessionRow.js toolbar has aria-label="Session actions"', () => {
    const src = readFileSync(SESSION_ROW_PATH, 'utf-8');
    expect(
      /aria-label="Session actions"/.test(src),
      'SessionRow.js toolbar must have aria-label="Session actions" per 06-CONTEXT.md line 78.',
    ).toBe(true);
  });

  test('structural: SessionRow.js uses 120ms opacity transition', () => {
    const src = readFileSync(SESSION_ROW_PATH, 'utf-8');
    expect(
      /duration-\[120ms\]/.test(src),
      'SessionRow.js toolbar must use `duration-[120ms]` opacity transition per 06-CONTEXT.md line 81.',
    ).toBe(true);
  });

  test('structural: SessionRow.js action buttons have focus-visible:opacity-100', () => {
    const src = readFileSync(SESSION_ROW_PATH, 'utf-8');
    expect(
      /focus-visible:opacity-100/.test(src),
      'SessionRow.js action buttons must have `focus-visible:opacity-100` so keyboard users see them on Tab.',
    ).toBe(true);
  });

  test('DOM: title span width exceeds half the row width at 1280x800', async ({ page }) => {
    await page.goto('/?t=test');
    await page.waitForSelector('header', { state: 'attached', timeout: 15000 });
    await page
      .waitForSelector('#preact-session-list', { state: 'attached', timeout: 15000 })
      .catch(() => {});
    const count = await page.locator('button[data-session-id]').count();
    test.skip(count === 0, 'no fixture sessions available — rely on structural tests');
    // Measure first non-selected row.
    const measurement = await page.evaluate(() => {
      const btn = document.querySelector('button[data-session-id]') as HTMLElement | null;
      if (!btn) return null;
      const titleSpan = btn.querySelector('span.truncate') as HTMLElement | null;
      if (!titleSpan) return null;
      const btnRect = btn.getBoundingClientRect();
      const titleRect = titleSpan.getBoundingClientRect();
      return { btnWidth: btnRect.width, titleWidth: titleRect.width };
    });
    test.skip(!measurement, 'no measurable row');
    if (measurement) {
      const ratio = measurement.titleWidth / measurement.btnWidth;
      expect(
        ratio,
        `title span width / row width = ${ratio.toFixed(2)} (title ${measurement.titleWidth}px of ${measurement.btnWidth}px). ` +
          `After WEB-P0-3 fix, the title must occupy >= 50% of the row width (previously ~35% due to 186px reserved for buttons).`,
      ).toBeGreaterThanOrEqual(0.5);
    }
  });

  test('DOM: role="toolbar" with aria-label "Session actions" exists when a row is rendered', async ({
    page,
  }) => {
    await page.goto('/?t=test');
    await page.waitForSelector('header', { state: 'attached', timeout: 15000 });
    await page
      .waitForSelector('#preact-session-list', { state: 'attached', timeout: 15000 })
      .catch(() => {});
    // WEB-P0-4 prevention layer (06-05): the toolbar is now gated on
    // mutationsEnabledSignal. The manually-managed test server may be
    // running with webMutations=false, in which case the toolbar is
    // correctly removed from the DOM. This test is about the 06-03
    // toolbar structure, not the 06-05 gating, so force the signal on.
    await page.evaluate(async () => {
      const state: any = await import('/static/app/state.js');
      state.mutationsEnabledSignal.value = true;
    });
    await page.waitForTimeout(200);
    const rowCount = await page.locator('button[data-session-id]').count();
    test.skip(rowCount === 0, 'no fixture rows');
    const toolbar = page.locator('[role="toolbar"][aria-label="Session actions"]').first();
    await expect(toolbar).toHaveCount(1);
  });

  test('DOM: hover reveals toolbar (opacity goes from 0 to 1)', async ({ page }) => {
    await page.goto('/?t=test');
    await page.waitForSelector('header', { state: 'attached', timeout: 15000 });
    await page
      .waitForSelector('#preact-session-list', { state: 'attached', timeout: 15000 })
      .catch(() => {});
    // WEB-P0-4 prevention layer (06-05): force mutationsEnabled=true so
    // the toolbar renders; see the prior test for the explanation.
    await page.evaluate(async () => {
      const state: any = await import('/static/app/state.js');
      state.mutationsEnabledSignal.value = true;
    });
    await page.waitForTimeout(200);
    const rowCount = await page.locator('button[data-session-id]').count();
    test.skip(rowCount === 0, 'no fixture rows');
    const row = page.locator('button[data-session-id]').first();
    await row.hover();
    await page.waitForTimeout(200); // allow 120ms transition to complete
    const opacity = await page.evaluate(() => {
      const toolbar = document.querySelector(
        'button[data-session-id] [role="toolbar"]',
      ) as HTMLElement | null;
      if (!toolbar) return null;
      return getComputedStyle(toolbar).opacity;
    });
    expect(opacity, 'toolbar opacity must be 1 after hovering the row').toBe('1');
  });
});
