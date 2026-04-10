import { test, expect } from '@playwright/test';
import { readFileSync } from 'fs';
import { join } from 'path';

/**
 * Phase 9 / Plan 02 / Task 1: POL-5 locale-aware currency formatting.
 *
 * Regression guards:
 *   (1) CostDashboard.js uses `new Intl.NumberFormat(navigator.language, {
 *       style: 'currency', currency: 'USD' })` instead of `'$' + v.toFixed(2)`.
 *   (2) EXACTLY ONE `new Intl.NumberFormat(` construction exists in the file
 *       (memoization enforced — fresh formatter per render is expensive).
 *   (3) Neither the old `fmt(v)` string concat nor the chart y-axis tick
 *       callback string concat remain.
 *   (4) Locale-loose DOM assertions across en-US and de-DE verify actual
 *       rendering. Regex deliberately tolerates ICU version differences.
 *
 * TDD ORDER: committed in FAILING state in Task 1 of plan 09-02, then made
 * green by Task 3 (CostDashboard.js edit).
 */

const COST_DASHBOARD_PATH = join(
  __dirname, '..', '..', '..',
  'internal', 'web', 'static', 'app', 'CostDashboard.js',
);

test.describe('POL-5 structural guards', () => {
  test('structural: CostDashboard.js uses Intl.NumberFormat with navigator.language', () => {
    const src = readFileSync(COST_DASHBOARD_PATH, 'utf-8');
    expect(
      src.includes('new Intl.NumberFormat(navigator.language'),
      'CostDashboard.js must construct `new Intl.NumberFormat(navigator.language, ...)` to honor the user\'s locale.',
    ).toBe(true);
    expect(
      /style:\s*['"]currency['"]/.test(src),
      'CostDashboard.js formatter must specify `style: \'currency\'`.',
    ).toBe(true);
    expect(
      /currency:\s*['"]USD['"]/.test(src),
      'CostDashboard.js formatter must specify `currency: \'USD\'` (no conversion, only locale-specific symbol placement).',
    ).toBe(true);
  });

  test('structural: exactly ONE `new Intl.NumberFormat(` construction (memoization)', () => {
    const src = readFileSync(COST_DASHBOARD_PATH, 'utf-8');
    const matches = src.match(/new Intl\.NumberFormat\(/g) || [];
    expect(
      matches.length,
      `CostDashboard.js must construct Intl.NumberFormat exactly once (module-level memoization). Found ${matches.length} occurrences.`,
    ).toBe(1);
  });

  test('structural: old `$\'+toFixed(2)` string concat removed from both fmt and chart callback', () => {
    const src = readFileSync(COST_DASHBOARD_PATH, 'utf-8');
    // The old fmt body:   return '$' + (v || 0).toFixed(2)
    expect(
      src.includes("'$' + (v || 0).toFixed(2)"),
      'CostDashboard.js fmt() must no longer hardcode `\'$\' + (v || 0).toFixed(2)` — delegate to currencyFormatter.',
    ).toBe(false);
    // The old chart y-axis callback:  callback: v => '$' + v.toFixed(2)
    expect(
      src.includes("'$' + v.toFixed(2)"),
      'CostDashboard.js y-axis tick callback must no longer hardcode `\'$\' + v.toFixed(2)` — delegate to currencyFormatter.',
    ).toBe(false);
  });
});

test.describe('POL-5 locale formatting', () => {
  async function stubCostApis(page: import('@playwright/test').Page) {
    await page.route('**/api/costs/summary*', route => route.fulfill({
      status: 200,
      contentType: 'application/json',
      body: JSON.stringify({
        today_usd: 1234.56,
        week_usd: 0,
        month_usd: 0,
        projected_usd: 0,
        today_events: 0,
        week_events: 0,
        month_events: 0,
      }),
    }));
    await page.route('**/api/costs/daily*', route => route.fulfill({
      status: 200,
      contentType: 'application/json',
      body: JSON.stringify([]),
    }));
    await page.route('**/api/costs/models*', route => route.fulfill({
      status: 200,
      contentType: 'application/json',
      body: JSON.stringify({}),
    }));
  }

  async function openCostDashboard(page: import('@playwright/test').Page) {
    await page.goto('/');
    await page.waitForSelector('header', { state: 'attached', timeout: 15000 });
    // Click the Costs button in the topbar (desktop viewport, button has aria-label)
    const costsBtn = page.locator('header button[aria-label="Open cost dashboard"]').first();
    await expect(costsBtn).toHaveCount(1);
    await costsBtn.click();
    // Wait for summary card text to render (the Today label is a div)
    await page.waitForSelector('text=Today', { state: 'visible', timeout: 10000 });
  }

  test('en-US: Today card renders `$1,234.56`', async ({ page }, testInfo) => {
    test.skip(testInfo.project.name !== 'chromium-en-US', 'en-US only');
    await stubCostApis(page);
    await openCostDashboard(page);
    // Find the card containing the "Today" label, then read the large amount cell.
    const todayCard = page.locator('div').filter({ hasText: /^Today$/ }).first();
    // The amount div sits as the next sibling inside the same card wrapper. Walk up to the card root.
    const cardRoot = todayCard.locator('xpath=ancestor::div[contains(@class, "rounded-lg")][1]');
    const amountText = await cardRoot.innerText();
    // Loose regex for en-US: `$1,234.56` possibly with nbsp.
    expect(
      /\$[\s\u00a0]?1,234\.56/.test(amountText),
      `en-US Today card must match /\\$[\\s\\u00a0]?1,234\\.56/. Got: ${amountText}`,
    ).toBe(true);
  });

  test('de-DE: Today card renders locale-grouped digits with `,` decimal separator', async ({ page }, testInfo) => {
    test.skip(testInfo.project.name !== 'chromium-de-DE', 'de-DE only');
    await stubCostApis(page);
    await openCostDashboard(page);
    const todayCard = page.locator('div').filter({ hasText: /^Today$/ }).first();
    const cardRoot = todayCard.locator('xpath=ancestor::div[contains(@class, "rounded-lg")][1]');
    const amountText = await cardRoot.innerText();
    // Loose regex: digits grouped with `.` or a narrow no-break space (U+202F),
    // `,` decimal separator, and a currency marker (`$`, `US$`, or `€`).
    // ICU versions vary on whether they use `$` or `US$` for USD in de-DE.
    expect(
      /1[.\u202f]234,56[\s\u00a0](?:€|US\$|\$)/.test(amountText),
      `de-DE Today card must match /1[.\\u202f]234,56[\\s\\u00a0](?:€|US\\$|\\$)/. Got: ${amountText}`,
    ).toBe(true);
  });

  test('ja-JP (via navigator.language override): Today card contains digits and `$`', async ({ page, context }, testInfo) => {
    test.skip(testInfo.project.name !== 'chromium-en-US', 'run once (base project) with navigator.language overridden');
    await context.addInitScript(() => {
      Object.defineProperty(navigator, 'language', { get: () => 'ja-JP' });
      Object.defineProperty(navigator, 'languages', { get: () => ['ja-JP'] });
    });
    await stubCostApis(page);
    await openCostDashboard(page);
    const todayCard = page.locator('div').filter({ hasText: /^Today$/ }).first();
    const cardRoot = todayCard.locator('xpath=ancestor::div[contains(@class, "rounded-lg")][1]');
    const amountText = await cardRoot.innerText();
    // ja-JP formats USD with the `$` sign at the start, period decimal separator.
    expect(
      /\$[\s\u00a0]?1,234\.56/.test(amountText),
      `ja-JP Today card must contain a currency marker and grouped digits. Got: ${amountText}`,
    ).toBe(true);
  });
});
