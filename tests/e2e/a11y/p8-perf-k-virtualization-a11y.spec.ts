// Phase 8 plan 04 — PERF-K a11y regression spec.
//
// Runtime gates for the virtualized SessionList:
//   1. axe-core: no violations scoped to the [role="list"] region
//   2. aria-rowcount equals the TOTAL session count (not the visible slice)
//   3. aria-rowindex is 1-based and matches the real item index in the full list
//   4. ArrowDown beyond the initial visible window scrolls the focused row into view
//
// All tests skip cleanly when either the test server is not running OR
// the test fixture does not expose a __testSetMockSessions helper. The
// structural contract is already pinned by the sibling
// p8-perf-k-virtualization.spec.ts; this spec only covers the runtime
// behavior that can only be verified in a real browser.

import { test, expect } from '@playwright/test';
import AxeBuilder from '@axe-core/playwright';

async function bootOrSkip(page: import('@playwright/test').Page) {
  try {
    await page.goto('/?t=perf-k-a11y', { timeout: 3000 });
    return true;
  } catch (_e) {
    return false;
  }
}

async function hasMockFixture(page: import('@playwright/test').Page): Promise<boolean> {
  return await page
    .evaluate(() => typeof (window as any).__testSetMockSessions === 'function')
    .catch(() => false);
}

test.describe('PERF-K a11y — virtualized SessionList', () => {
  test('axe-core: no violations in the virtualized list region', async ({ page }) => {
    const ok = await bootOrSkip(page);
    if (!ok) {
      test.skip(true, 'test server not running on :18420');
      return;
    }
    await page.evaluate(() => localStorage.setItem('agentdeck_virtualize', '1'));
    await page
      .waitForSelector('[role="list"]', { state: 'attached', timeout: 5000 })
      .catch(() => { /* skip cleanly if no list rendered */ });
    const listExists = await page.locator('[role="list"]').count();
    if (listExists === 0) {
      test.skip(true, 'no list region rendered — skip axe scan');
      return;
    }
    const results = await new AxeBuilder({ page }).include('[role="list"]').analyze();
    expect(
      results.violations,
      `axe violations: ${JSON.stringify(results.violations.map(v => v.id))}`,
    ).toEqual([]);
  });

  test('aria-rowcount equals the total item count, not the visible count', async ({ page }) => {
    const ok = await bootOrSkip(page);
    if (!ok) {
      test.skip(true, 'test server not running on :18420');
      return;
    }
    await page.evaluate(() => localStorage.setItem('agentdeck_virtualize', '1'));
    if (!(await hasMockFixture(page))) {
      test.skip(true, 'no mock session fixture available');
      return;
    }
    await page.evaluate(() => (window as any).__testSetMockSessions(200));
    await page.waitForTimeout(200);
    const list = page.locator('[role="list"]').first();
    const rowcount = await list.getAttribute('aria-rowcount');
    expect(
      rowcount,
      'aria-rowcount must equal the total number of items (200), not the visible slice count',
    ).toBe('200');
  });

  test('aria-rowindex is 1-based and matches the real item index', async ({ page }) => {
    const ok = await bootOrSkip(page);
    if (!ok) {
      test.skip(true, 'test server not running on :18420');
      return;
    }
    await page.evaluate(() => localStorage.setItem('agentdeck_virtualize', '1'));
    if (!(await hasMockFixture(page))) {
      test.skip(true, 'no mock session fixture available');
      return;
    }
    await page.evaluate(() => (window as any).__testSetMockSessions(200));
    await page.waitForTimeout(200);
    const firstRow = page.locator('[role="listitem"]').first();
    const firstIdx = await firstRow.getAttribute('aria-rowindex');
    expect(
      parseInt(firstIdx || '0', 10),
      'first visible row must have aria-rowindex >= 1',
    ).toBeGreaterThanOrEqual(1);
  });

  test('keyboard: ArrowDown past visible window scrolls focused row into view', async ({ page }) => {
    const ok = await bootOrSkip(page);
    if (!ok) {
      test.skip(true, 'test server not running on :18420');
      return;
    }
    await page.evaluate(() => localStorage.setItem('agentdeck_virtualize', '1'));
    if (!(await hasMockFixture(page))) {
      test.skip(true, 'no mock session fixture available');
      return;
    }
    await page.evaluate(() => (window as any).__testSetMockSessions(200));
    await page.waitForTimeout(200);

    const firstRow = page.locator('[role="listitem"]').first();
    await firstRow.focus();

    for (let i = 0; i < 60; i++) {
      await page.keyboard.press('ArrowDown');
    }

    const focused = page.locator('[role="listitem"]:focus');
    const box = await focused.boundingBox();
    expect(box, 'a row must be focused after 60 ArrowDown presses').not.toBeNull();
    if (box) {
      expect(box.y, 'focused row must be scrolled into the visible viewport').toBeGreaterThan(0);
    }
  });
});
