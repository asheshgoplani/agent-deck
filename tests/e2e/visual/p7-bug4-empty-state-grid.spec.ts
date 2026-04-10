import { test, expect } from '@playwright/test';
import { readFileSync } from 'fs';
import { join } from 'path';

/**
 * Phase 7 / Plan 02 / Task 1b: WEB-P1-4 regression test
 *
 * Asserts that EmptyStateDashboard renders a card-grid layout with a
 * max-w-4xl container, three top stat cards (single column on mobile,
 * three columns on lg+), and is horizontally centered on big monitors.
 *
 * Root cause (LOCKED per 07-02-PLAN.md): the current EmptyStateDashboard
 * is a flat vertical stack of `flex flex-col items-center justify-center`
 * with no max-width -- on a 1920x1080 monitor it floats in a sea of gray.
 *
 * Fix (LOCKED): wrap the dashboard in `max-w-4xl mx-auto`, render the
 * stats as `grid grid-cols-1 lg:grid-cols-3` cards, and add a
 * `data-testid="empty-state-dashboard"` for DOM assertions.
 *
 * TDD ORDER: failing in Task 1b, green in Task 3.
 */

const DASHBOARD_PATH = join(
  __dirname, '..', '..', '..',
  'internal', 'web', 'static', 'app', 'EmptyStateDashboard.js',
);

test.describe('WEB-P1-4 -- empty-state dashboard uses card grid with max-w-4xl', () => {
  // ===== Layer 1: structural =====

  test('structural: EmptyStateDashboard.js contains max-w-4xl', () => {
    const src = readFileSync(DASHBOARD_PATH, 'utf-8');
    expect(
      src.includes('max-w-4xl'),
      'EmptyStateDashboard.js must wrap content in `max-w-4xl` per WEB-P1-4.',
    ).toBe(true);
  });

  test('structural: EmptyStateDashboard.js contains mx-auto', () => {
    const src = readFileSync(DASHBOARD_PATH, 'utf-8');
    expect(
      src.includes('mx-auto'),
      'EmptyStateDashboard.js must center the container with `mx-auto` per WEB-P1-4.',
    ).toBe(true);
  });

  test('structural: EmptyStateDashboard.js uses responsive grid (grid-cols-1 lg:grid-cols-3)', () => {
    const src = readFileSync(DASHBOARD_PATH, 'utf-8');
    expect(
      /grid-cols-1/.test(src),
      'EmptyStateDashboard.js must use `grid-cols-1` for mobile single-column layout.',
    ).toBe(true);
    expect(
      /lg:grid-cols-3/.test(src),
      'EmptyStateDashboard.js must use `lg:grid-cols-3` for desktop three-column stats grid.',
    ).toBe(true);
  });

  test('structural: stat labels Running/Waiting/Error preserved (no regression)', () => {
    const src = readFileSync(DASHBOARD_PATH, 'utf-8');
    expect(src.includes('Running')).toBe(true);
    expect(src.includes('Waiting')).toBe(true);
    expect(src.includes('Error')).toBe(true);
  });

  test('structural: data-testid="empty-state-dashboard" present', () => {
    const src = readFileSync(DASHBOARD_PATH, 'utf-8');
    expect(
      src.includes('data-testid="empty-state-dashboard"'),
      'EmptyStateDashboard.js must add a data-testid for DOM assertions.',
    ).toBe(true);
  });

  // ===== Layer 2: DOM =====

  test('DOM 1920x1080: dashboard is constrained to <=1024 px wide (max-w-4xl ~ 896 px)', async ({ page }) => {
    await page.setViewportSize({ width: 1920, height: 1080 });
    await page.goto('/?t=test');
    await page.waitForSelector('header', { state: 'attached', timeout: 15000 });
    // Empty state requires no session selected. Force /?t=test root URL has no session id.
    await page.evaluate(() => { window.history.replaceState(null, '', '/'); });
    await page.waitForTimeout(300);
    const dashboard = page.locator('[data-testid="empty-state-dashboard"]');
    const count = await dashboard.count();
    test.skip(count === 0, 'empty state not rendered (a session was auto-selected); skip DOM measurement');
    const box = await dashboard.boundingBox();
    expect(box, 'dashboard must have a bounding box').not.toBeNull();
    if (box) {
      // max-w-4xl = 56rem = 896 px; allow 1024 to give breathing room for paddings.
      expect(
        box.width,
        `at 1920x1080, dashboard width must be <=1024 px (max-w-4xl); got ${box.width}`,
      ).toBeLessThanOrEqual(1024);
    }
  });

  test('DOM 1920x1080: dashboard is horizontally centered within main', async ({ page }) => {
    await page.setViewportSize({ width: 1920, height: 1080 });
    await page.goto('/?t=test');
    await page.waitForSelector('header', { state: 'attached', timeout: 15000 });
    await page.evaluate(() => { window.history.replaceState(null, '', '/'); });
    await page.waitForTimeout(300);
    const dashboard = page.locator('[data-testid="empty-state-dashboard"]');
    const count = await dashboard.count();
    test.skip(count === 0, 'empty state not rendered; skip centering measurement');
    const main = page.locator('main');
    const dBox = await dashboard.boundingBox();
    const mBox = await main.boundingBox();
    expect(dBox && mBox).toBeTruthy();
    if (dBox && mBox) {
      const leftGap = dBox.x - mBox.x;
      const rightGap = (mBox.x + mBox.width) - (dBox.x + dBox.width);
      expect(
        Math.abs(leftGap - rightGap),
        `dashboard must be horizontally centered (left=${leftGap}, right=${rightGap}, diff=${Math.abs(leftGap - rightGap)})`,
      ).toBeLessThanOrEqual(32);
    }
  });
});
