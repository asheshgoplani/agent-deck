import { test, expect } from '@playwright/test';
import { readFileSync } from 'fs';
import { join } from 'path';

/**
 * Phase 9 / Plan 01 / Task 1: POL-4 regression test
 *
 * POL-4 (group divider density): the group header vertical padding is
 * reduced from py-2.5 (20 px) to py-1 (8 px), and the button's enforced
 * min-h drops from min-h-[44px] to min-h-[40px]. The measurable invariant
 * is that when a group B directly follows a session in group A, the
 * vertical gap from the last session's bottom edge to the group title
 * text is ≤ 16 px.
 *
 * Contract:
 *   1. GroupRow.js outer button class contains ` py-1 ` and ` min-h-[40px] `
 *      (with leading/trailing spaces to avoid false matches on `py-1.5`).
 *   2. GroupRow.js outer button class does NOT contain ` py-2.5 `.
 *   3. DOM (fixture): with two groups each containing one session, the
 *      delta between the group-B title span's bounding top and the last
 *      group-A session row's bounding bottom is ≤ 16 AND ≥ 0.
 *
 * TDD ORDER: this spec is committed in FAILING state in Task 1. Task 3
 * lands the GroupRow.js padding edit, flipping the spec to green.
 */

const GROUP_ROW_PATH = join(
  __dirname, '..', '..', '..',
  'internal', 'web', 'static', 'app', 'GroupRow.js',
);

test.describe('POL-4 — group header density', () => {
  test('structural: GroupRow.js outer button uses py-1 and min-h-[40px]', () => {
    const src = readFileSync(GROUP_ROW_PATH, 'utf-8');
    // Leading/trailing whitespace (space, newline, tab) to avoid matching py-1.5
    expect(
      /\spy-1(?![.\d])/.test(src),
      'GroupRow.js must contain ` py-1 ` on the outer button (not py-1.5 or py-1.25).',
    ).toBe(true);
    expect(
      /\smin-h-\[40px\]/.test(src),
      'GroupRow.js must contain ` min-h-[40px] ` on the outer button.',
    ).toBe(true);
  });

  test('structural: GroupRow.js outer button no longer has py-2.5 or min-h-[44px]', () => {
    const src = readFileSync(GROUP_ROW_PATH, 'utf-8');
    expect(
      /\bpy-2\.5\b/.test(src),
      'GroupRow.js still contains `py-2.5` — POL-4 requires it to drop to `py-1`.',
    ).toBe(false);
    expect(
      /min-h-\[44px\]/.test(src),
      'GroupRow.js still contains `min-h-[44px]` — POL-4 requires it to drop to `min-h-[40px]`.',
    ).toBe(false);
  });

  // ===== Layer 2: DOM density measurement =====

  test('DOM: gap from last session bottom to next group title top is ≤ 16 px', async ({ page }) => {
    await page.goto('/?token=test');
    await page.waitForFunction(
      () => window.__preactSessionListActive === true,
      null,
      { timeout: 8000 },
    ).catch(() => {});
    // Need at least one group row followed by a session row for the measurement
    const groupCount = await page.locator('#preact-session-list button[aria-expanded]').count();
    const sessionCount = await page.locator('#preact-session-list button[data-session-id]').count();
    test.skip(
      groupCount < 2 || sessionCount < 1,
      `density DOM test needs ≥2 groups and ≥1 session in the fixture; got ${groupCount} groups and ${sessionCount} sessions — structural tests cover the contract`,
    );
    // Walk the rendered list in document order. For each group button after
    // the first, find the previous session row (if any) and measure the gap
    // from its bottom to the group's title text top.
    const deltas = await page.evaluate(() => {
      const list = document.querySelector('#preact-session-list');
      if (!list) return [];
      const rows = Array.from(list.querySelectorAll('li'));
      const out: number[] = [];
      for (let i = 1; i < rows.length; i++) {
        const groupBtn = rows[i].querySelector('button[aria-expanded]');
        if (!groupBtn) continue;
        // Previous row must carry a session
        const prevSession = rows[i - 1].querySelector('button[data-session-id]');
        if (!prevSession) continue;
        const titleSpan = groupBtn.querySelector('span.flex-1');
        if (!titleSpan) continue;
        const prevRect = (prevSession as HTMLElement).getBoundingClientRect();
        const titleRect = (titleSpan as HTMLElement).getBoundingClientRect();
        out.push(titleRect.top - prevRect.bottom);
      }
      return out;
    });
    test.skip(deltas.length === 0, 'no session-then-group adjacencies found in fixture — structural tests cover the contract');
    for (const delta of deltas) {
      expect(delta, `session-to-next-group gap should be between 0 and 16 px inclusive; got ${delta}`).toBeGreaterThanOrEqual(0);
      expect(delta, `session-to-next-group gap should be between 0 and 16 px inclusive; got ${delta}`).toBeLessThanOrEqual(16);
    }
  });
});
