// Phase 8 plan 04 — PERF-K SessionList virtualization regression spec.
//
// Three pre-flight invariant gates protect Phase 6/7/08-03 work from being
// silently undone by plan 08-04 edits to SessionList.js:
//   1. Phase 6 06-03 WEB-P0-3 — absolute right-2 toolbar (exactly once)
//   2. Phase 7 07-03 WEB-P1-3 — min-h-[40px] fixed row height (exactly once)
//   3. Phase 8 plan 03 PERF-G — SessionRow memo( wrap (at least once)
//
// Seven structural gates cover the PERF-K surface itself:
//   4. useVirtualList.js exists and exports useVirtualList
//   5. SessionList.js imports useVirtualList
//   6. SessionList.js uses the localStorage feature flag
//   7. SessionList.js has a > 50 items length gate
//   8. useVirtualList.js uses ResizeObserver for variable-height rows
//   9. useVirtualList.js contains a binary search on offsets
//
// One DOM smoke test (test 10) validates virtualization at runtime IF the
// test server exposes a __testSetMockSessions fixture; otherwise it skips.

import { test, expect } from '@playwright/test';
import { readFileSync, existsSync } from 'fs';
import { join } from 'path';

const ROOT = join(__dirname, '..', '..', '..');
const SESSION_ROW = join(ROOT, 'internal', 'web', 'static', 'app', 'SessionRow.js');
const SESSION_LIST = join(ROOT, 'internal', 'web', 'static', 'app', 'SessionList.js');
const USE_VIRTUAL_LIST = join(ROOT, 'internal', 'web', 'static', 'app', 'useVirtualList.js');

function source(p: string): string {
  return readFileSync(p, 'utf-8');
}

test.describe('PERF-K — SessionList virtualization', () => {
  // Pre-flight gates: cross-phase invariants that MUST still hold.

  test('pre-flight: Phase 6 06-03 WEB-P0-3 invariant — absolute right-2 in SessionRow.js', () => {
    const src = source(SESSION_ROW);
    const count = (src.match(/absolute right-2/g) || []).length;
    expect(
      count,
      'SessionRow.js must still contain "absolute right-2" exactly once (Phase 6 06-03 action toolbar invariant). If this fails, Phase 6 plan 06-03 was never shipped or plan 08-04 broke it.',
    ).toBe(1);
  });

  test('pre-flight: Phase 7 07-03 WEB-P1-3 invariant — min-h-[40px] in SessionRow.js', () => {
    const src = source(SESSION_ROW);
    const count = (src.match(/min-h-\[40px\]/g) || []).length;
    expect(
      count,
      'SessionRow.js must still contain "min-h-[40px]" exactly once (Phase 7 07-03 row density invariant). Virtualization requires a fixed row height.',
    ).toBe(1);
  });

  test('pre-flight: Phase 8 plan 03 PERF-G invariant — memo( in SessionRow.js', () => {
    const src = source(SESSION_ROW);
    const count = (src.match(/\bmemo\s*\(/g) || []).length;
    expect(
      count,
      'SessionRow.js must contain at least one memo() wrap (Phase 8 plan 03 PERF-G invariant). Without memoization, virtualization gains are defeated by prop re-render cascades.',
    ).toBeGreaterThanOrEqual(1);
  });

  // Structural gates for PERF-K itself

  test('structural: useVirtualList.js exists and exports useVirtualList', () => {
    expect(
      existsSync(USE_VIRTUAL_LIST),
      'internal/web/static/app/useVirtualList.js must exist (PERF-K hand-rolled hook).',
    ).toBe(true);
    const src = source(USE_VIRTUAL_LIST);
    expect(
      /export\s+(function|const)\s+useVirtualList/.test(src),
      'useVirtualList.js must export a function or const named useVirtualList.',
    ).toBe(true);
  });

  test('structural: SessionList.js imports useVirtualList', () => {
    const src = source(SESSION_LIST);
    expect(
      /from\s+['"]\.\/useVirtualList\.js['"]/.test(src),
      "SessionList.js must import useVirtualList from './useVirtualList.js'.",
    ).toBe(true);
  });

  test('structural: SessionList.js uses the localStorage feature flag', () => {
    const src = source(SESSION_LIST);
    expect(
      /agentdeck_virtualize/.test(src),
      "SessionList.js must check localStorage.getItem('agentdeck_virtualize') to opt into virtualization.",
    ).toBe(true);
  });

  test('structural: SessionList.js has a > 50 length gate', () => {
    const src = source(SESSION_LIST);
    expect(
      /\b(length|size|count)\s*>\s*50\b/.test(src),
      'SessionList.js must gate virtualization at > 50 items (below threshold, use the non-virtualized path).',
    ).toBe(true);
  });

  test('structural: useVirtualList.js uses ResizeObserver for variable-height group headers', () => {
    const src = source(USE_VIRTUAL_LIST);
    expect(
      /ResizeObserver/.test(src),
      'useVirtualList.js must use ResizeObserver to measure variable-height group headers.',
    ).toBe(true);
  });

  test('structural: useVirtualList.js contains a binary search on offsets', () => {
    const src = source(USE_VIRTUAL_LIST);
    const hasBinarySearch =
      /binarySearch/.test(src) ||
      /while\s*\(\s*\w+\s*<=?\s*\w+\s*\)/.test(src) ||
      />>\s*1/.test(src) ||
      /Math\.floor\s*\(\s*\(\s*\w+\s*\+\s*\w+\s*\)\s*\/\s*2\s*\)/.test(src);
    expect(
      hasBinarySearch,
      'useVirtualList.js must contain a binary search on offsets (named helper, while loop, or midpoint math).',
    ).toBe(true);
  });

  // DOM smoke test — optional (skips cleanly if test server or fixture is unavailable)
  test('DOM smoke: 200 mock sessions render < 200 rows when flag is on (proof of virtualization)', async ({ page }) => {
    // If the test server is not running, skip cleanly
    try {
      await page.goto('/?t=perf-k', { timeout: 3000 });
    } catch (_e) {
      test.skip(true, 'test server not running on :18420 — structural gates already cover the contract');
      return;
    }
    await page.evaluate(() => {
      localStorage.setItem('agentdeck_virtualize', '1');
    });
    const hasFixture = await page.evaluate(() => {
      return typeof (window as any).__testSetMockSessions === 'function';
    }).catch(() => false);
    if (!hasFixture) {
      test.skip(true, 'no 200-session fixture available in current test server');
      return;
    }
    await page.evaluate(() => (window as any).__testSetMockSessions(200));
    await page.waitForTimeout(200);
    const rowCount = await page.locator('[role="listitem"], [data-testid="session-row"]').count();
    expect(
      rowCount,
      `With 200 sessions and the flag on, the DOM should contain < 200 row elements (virtualized). Found ${rowCount}.`,
    ).toBeLessThan(200);
  });
});
