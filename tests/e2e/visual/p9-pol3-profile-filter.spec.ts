import { test, expect } from '@playwright/test';
import { readFileSync } from 'fs';
import { join } from 'path';

/**
 * Phase 9 / Plan 02 / Task 1: POL-3 profile dropdown filter + max-height.
 *
 * Regression guards:
 *   (1) ProfileDropdown.js filters `_*` profiles from the rendered listbox
 *       (internal test profiles like `_test`, `_dev`, `_baseline_test`,
 *       `_webfulltest` should never appear to the user).
 *   (2) The multi-profile listbox container class string contains
 *       `max-h-[300px] overflow-y-auto` so long lists scroll rather than
 *       pushing the viewport.
 *   (3) WEB-P0-2 Option B scaffolding from plan 06-01 is intact:
 *       `role="status"` (single-profile path), `aria-haspopup="listbox"`
 *       (multi-profile path), and the `HELP_TEXT` constant/string.
 *
 * DOM assertions mock /api/profiles via page.route so the test is
 * deterministic across local dev (many `_*` profiles) and CI (few profiles).
 *
 * TDD ORDER: committed in FAILING state in Task 1 of plan 09-02, then made
 * green by Task 2 (ProfileDropdown.js edit + make css).
 */

const PROFILE_DROPDOWN_PATH = join(
  __dirname, '..', '..', '..',
  'internal', 'web', 'static', 'app', 'ProfileDropdown.js',
);

test.describe('POL-3 structural guards', () => {
  test('structural: ProfileDropdown.js filters `_*` profiles from the list', () => {
    const src = readFileSync(PROFILE_DROPDOWN_PATH, 'utf-8');
    // Match either `p =>` or `(p) =>` arrow styles.
    const hasFilter = /filter\s*\(\s*\(?p\)?\s*=>\s*!p\.startsWith\(\s*['"]_['"]\s*\)\s*\)/.test(src);
    expect(
      hasFilter,
      'ProfileDropdown.js must filter internal `_*` profiles out of the /api/profiles response before rendering the listbox. Expected pattern: filter(p => !p.startsWith(\'_\'))',
    ).toBe(true);
  });

  test('structural: multi-profile listbox container has max-h-[300px] overflow-y-auto', () => {
    const src = readFileSync(PROFILE_DROPDOWN_PATH, 'utf-8');
    expect(
      src.includes('max-h-[300px]'),
      'ProfileDropdown.js multi-profile listbox container must include `max-h-[300px]` so long lists scroll instead of pushing the viewport.',
    ).toBe(true);
    expect(
      src.includes('overflow-y-auto'),
      'ProfileDropdown.js multi-profile listbox container must include `overflow-y-auto` so the max-height actually scrolls.',
    ).toBe(true);
  });

  test('structural: WEB-P0-2 Option B scaffolding preserved (role="status", aria-haspopup, HELP_TEXT)', () => {
    const src = readFileSync(PROFILE_DROPDOWN_PATH, 'utf-8');
    expect(
      src.includes('role="status"'),
      'ProfileDropdown.js must still render role="status" on the single-profile path per WEB-P0-2 Option B (plan 06-01 invariant).',
    ).toBe(true);
    expect(
      src.includes('aria-haspopup="listbox"'),
      'ProfileDropdown.js must still render aria-haspopup="listbox" on the multi-profile path per WEB-P0-2 Option B (plan 06-01 invariant).',
    ).toBe(true);
    expect(
      src.includes('HELP_TEXT'),
      'ProfileDropdown.js must still reference the HELP_TEXT constant per WEB-P0-2 Option B always-visible help line (plan 06-01 invariant).',
    ).toBe(true);
  });
});

test.describe('POL-3 DOM behavior', () => {
  test('DOM: 12 profiles with 2 `_*` prefix renders 10 visible options and hides `_test`/`_dev`', async ({ page }) => {
    await page.route('**/api/profiles*', route => route.fulfill({
      status: 200,
      contentType: 'application/json',
      body: JSON.stringify({
        current: 'default',
        profiles: [
          'default', 'work', '_test', '_dev', 'alpha', 'beta',
          'gamma', 'delta', 'epsilon', 'zeta', 'eta', 'theta',
        ],
      }),
    }));
    await page.goto('/');
    await page.waitForSelector('header', { state: 'attached', timeout: 15000 });
    // Open the dropdown
    const button = page.locator('header button[aria-haspopup="listbox"]').first();
    await expect(button).toHaveCount(1);
    await button.click();
    // Wait for listbox to open
    const listbox = page.locator('[role="listbox"][aria-label="Available profiles (read-only)"]');
    await expect(listbox).toBeVisible();
    // Count options: should be 10, not 12
    const options = listbox.locator('[role="option"]');
    await expect(options).toHaveCount(10);
    // `_test` and `_dev` must NOT appear
    await expect(listbox.locator('[role="option"]', { hasText: /^_test$/ })).toHaveCount(0);
    await expect(listbox.locator('[role="option"]', { hasText: /^_dev$/ })).toHaveCount(0);
  });

  test('DOM: listbox has computed max-height 300px and scrolls when content exceeds it', async ({ page }) => {
    // Route with many profiles so the listbox actually overflows its max-height.
    const manyProfiles = ['default'];
    for (let i = 0; i < 25; i++) manyProfiles.push(`profile-${i}`);
    manyProfiles.push('_test', '_dev'); // will be filtered
    await page.route('**/api/profiles*', route => route.fulfill({
      status: 200,
      contentType: 'application/json',
      body: JSON.stringify({ current: 'default', profiles: manyProfiles }),
    }));
    await page.goto('/');
    await page.waitForSelector('header', { state: 'attached', timeout: 15000 });
    const button = page.locator('header button[aria-haspopup="listbox"]').first();
    await button.click();
    const listbox = page.locator('[role="listbox"][aria-label="Available profiles (read-only)"]');
    await expect(listbox).toBeVisible();
    // Computed max-height must be 300px
    const computedMaxHeight = await listbox.evaluate(
      el => window.getComputedStyle(el).maxHeight,
    );
    expect(computedMaxHeight, `listbox max-height must be 300px; got ${computedMaxHeight}`).toBe('300px');
    // Scroll must be active (scrollHeight > clientHeight)
    const dims = await listbox.evaluate(el => ({
      scrollHeight: el.scrollHeight,
      clientHeight: el.clientHeight,
    }));
    expect(
      dims.scrollHeight,
      `listbox scrollHeight must exceed clientHeight when 25 options render in a 300px box. scrollHeight=${dims.scrollHeight} clientHeight=${dims.clientHeight}`,
    ).toBeGreaterThan(dims.clientHeight);
  });

  test('DOM: filtering down to 1 profile renders the single-profile status fallback', async ({ page }) => {
    await page.route('**/api/profiles*', route => route.fulfill({
      status: 200,
      contentType: 'application/json',
      body: JSON.stringify({
        current: 'default',
        // After filter this becomes ['default'] (length 1) → single-profile branch.
        profiles: ['default', '_test', '_dev'],
      }),
    }));
    await page.goto('/');
    await page.waitForSelector('header', { state: 'attached', timeout: 15000 });
    // Single-profile branch MUST render role="status" with data-testid
    const status = page.locator('[role="status"][data-testid="profile-indicator"]');
    await expect(status).toHaveCount(1);
    // Multi-profile button MUST NOT be present
    const multi = page.locator('header button[aria-haspopup="listbox"]');
    await expect(multi).toHaveCount(0);
  });
});
