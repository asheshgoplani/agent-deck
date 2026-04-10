import { test, expect } from '@playwright/test';
import { readFileSync } from 'fs';
import { join } from 'path';

/**
 * Phase 9 / Plan 01 / Task 1: POL-2 regression test
 *
 * POL-2 (120ms opacity fade on action buttons): GroupRow.js action cluster
 * must use opacity + pointer-events + group-hover/group-focus-within with a
 * `transition-opacity duration-[120ms] motion-reduce:transition-none`
 * pattern. The old snap-based `hidden group-hover:flex` must be gone.
 *
 * SessionRow.js (touched by plan 06-03) already has the 120ms wiring — this
 * spec installs a REGRESSION GUARD for that wiring so a future refactor
 * cannot silently strip it.
 *
 * Contract:
 *   1. GroupRow.js action-cluster class contains: `transition-opacity`,
 *      `duration-[120ms]`, `motion-reduce:transition-none`, `opacity-0`,
 *      `group-hover:opacity-100`, `pointer-events-none`,
 *      `group-hover:pointer-events-auto`, `group-focus-within:opacity-100`,
 *      `group-focus-within:pointer-events-auto`.
 *   2. GroupRow.js does NOT contain `hidden group-hover:flex` anywhere.
 *   3. SessionRow.js retains its 06-03 toolbar wiring:
 *      `transition-opacity duration-[120ms] motion-reduce:transition-none`.
 *
 * TDD ORDER: this spec is committed in FAILING state in Task 1. Task 3
 * lands the GroupRow.js class string swap, flipping the spec to green.
 */

const GROUP_ROW_PATH = join(
  __dirname, '..', '..', '..',
  'internal', 'web', 'static', 'app', 'GroupRow.js',
);
const SESSION_ROW_PATH = join(
  __dirname, '..', '..', '..',
  'internal', 'web', 'static', 'app', 'SessionRow.js',
);

test.describe('POL-2 — 120ms opacity fade on action clusters', () => {
  test('structural: GroupRow.js action cluster has the full opacity-fade class list', () => {
    const src = readFileSync(GROUP_ROW_PATH, 'utf-8');
    const required = [
      'transition-opacity',
      'duration-[120ms]',
      'motion-reduce:transition-none',
      'opacity-0',
      'group-hover:opacity-100',
      'pointer-events-none',
      'group-hover:pointer-events-auto',
      'group-focus-within:opacity-100',
      'group-focus-within:pointer-events-auto',
    ];
    for (const token of required) {
      expect(
        src.includes(token),
        `GroupRow.js must contain \`${token}\` on the action cluster. Missing: ${token}`,
      ).toBe(true);
    }
  });

  test('structural: GroupRow.js does NOT contain `hidden group-hover:flex` (snap show/hide is gone)', () => {
    const src = readFileSync(GROUP_ROW_PATH, 'utf-8');
    expect(
      src.includes('hidden group-hover:flex'),
      'GroupRow.js still contains `hidden group-hover:flex` — the snap-based display toggle must be replaced with the opacity fade (display is not transitionable).',
    ).toBe(false);
  });

  test('regression guard: SessionRow.js retains 06-03 toolbar transition-opacity duration-[120ms] motion-reduce wiring', () => {
    const src = readFileSync(SESSION_ROW_PATH, 'utf-8');
    expect(
      /transition-opacity\s+duration-\[120ms\]\s+motion-reduce:transition-none/.test(src),
      'SessionRow.js must retain the 06-03 toolbar wiring `transition-opacity duration-[120ms] motion-reduce:transition-none`. If this fails, a future refactor has silently snapped the toolbar back to non-animated show/hide.',
    ).toBe(true);
  });

  // ===== Layer 2: DOM computed-style checks =====

  test('DOM: GroupRow action cluster computed transition-property contains opacity on hover-capable viewports', async ({ page }) => {
    await page.goto('/?token=test');
    // Wait for Preact to mount
    await page.waitForFunction(
      () => window.__preactSessionListActive === true,
      null,
      { timeout: 8000 },
    ).catch(() => {
      // If no sessions mount, we can't do the DOM assertion — structural tests cover the contract.
    });
    const groupCount = await page.locator('#preact-session-list button[aria-expanded]').count();
    test.skip(groupCount === 0, 'no fixture groups — structural tests cover the contract');
    // Find the action cluster span inside the first group row
    const actionSpan = page.locator('#preact-session-list button[aria-expanded]').first().locator('span').filter({
      has: page.locator('button[aria-label="Create subgroup"]'),
    });
    const transitionProp = await actionSpan.evaluate((el) => window.getComputedStyle(el).transitionProperty);
    expect(
      transitionProp.includes('opacity') || transitionProp === 'all',
      `GroupRow action cluster transition-property must include 'opacity' (or be 'all'); got '${transitionProp}'`,
    ).toBe(true);
  });

  test('DOM: reduced-motion emulation makes GroupRow action cluster transition-property `none`', async ({ page, browserName }) => {
    test.skip(browserName !== 'chromium', 'emulateMedia reducedMotion is chromium-only in this config');
    await page.emulateMedia({ reducedMotion: 'reduce' });
    await page.goto('/?token=test');
    await page.waitForFunction(
      () => window.__preactSessionListActive === true,
      null,
      { timeout: 8000 },
    ).catch(() => {});
    const groupCount = await page.locator('#preact-session-list button[aria-expanded]').count();
    test.skip(groupCount === 0, 'no fixture groups — structural tests cover the contract');
    const actionSpan = page.locator('#preact-session-list button[aria-expanded]').first().locator('span').filter({
      has: page.locator('button[aria-label="Create subgroup"]'),
    });
    const transitionProp = await actionSpan.evaluate((el) => window.getComputedStyle(el).transitionProperty);
    expect(
      transitionProp === 'none',
      `with prefers-reduced-motion: reduce, GroupRow action cluster transition-property must be 'none' (motion-reduce variant wins); got '${transitionProp}'`,
    ).toBe(true);
  });
});
