import type { Page, Locator } from '@playwright/test';

/**
 * CSS injected to kill ALL animations and transitions instantly.
 * Applied via page.addStyleTag() before every screenshot.
 * Addresses Pitfall 6 (animations not stopped) and Pitfall 19 (skeleton flake).
 */
const KILL_ANIMATIONS_CSS = `
  *, *::before, *::after {
    animation-duration: 0ms !important;
    animation-delay: 0ms !important;
    transition-duration: 0ms !important;
    transition-delay: 0ms !important;
    scroll-behavior: auto !important;
  }
`;

/**
 * Inject a <style> tag that kills all CSS animations and transitions.
 * Must be called BEFORE any screenshot capture. The injected style tag
 * uses `!important` to override any inline or utility styles (e.g.,
 * Tailwind's `animate-pulse` on skeleton loaders, `duration-[120ms]`
 * on action button fades).
 */
export async function killAnimations(page: Page): Promise<void> {
  await page.addStyleTag({ content: KILL_ANIMATIONS_CSS });
  // Force layout flush so the browser applies the override before screenshot
  await page.evaluate(() => {
    void document.body.getBoundingClientRect();
    return new Promise<void>((resolve) => {
      requestAnimationFrame(() => {
        requestAnimationFrame(() => resolve());
      });
    });
  });
}

/**
 * Freeze the page clock to a deterministic timestamp.
 * Must be called BEFORE page.goto() — Playwright's clock.install()
 * must run before page scripts execute to intercept Date, setTimeout,
 * setInterval, requestAnimationFrame, and performance.now().
 *
 * Uses 2026-01-01T00:00:00Z as the frozen time so any timestamp
 * rendered in the UI is deterministic across runs.
 */
export async function freezeClock(page: Page): Promise<void> {
  await page.clock.install({ time: new Date('2026-01-01T00:00:00Z') });
}

/**
 * Build an array of Locator objects pointing at dynamic content
 * elements that should be masked in screenshots. Dynamic content
 * includes timestamps, cost values, session IDs, connection status
 * indicators, and version strings.
 *
 * The mask array is passed to `page.screenshot({ mask: [...] })`.
 * Playwright replaces masked regions with a solid color block in
 * the screenshot, eliminating false positives from changing data.
 *
 * Only returns locators for elements that actually exist on the page
 * (filters out stale selectors via isVisible check).
 */
export async function getDynamicContentMasks(page: Page): Promise<Locator[]> {
  const selectors = [
    '[data-testid="connection-indicator"]',
    '[data-testid="cost-today"]',
    '[data-testid="cost-week"]',
    '[data-testid="cost-month"]',
    '[data-testid="cost-projected"]',
    '[data-testid="profile-indicator"]',
    'time',
    '[data-testid="session-cost"]',
    '[data-testid="version-string"]',
  ];

  const masks: Locator[] = [];
  for (const sel of selectors) {
    const locator = page.locator(sel);
    const count = await locator.count();
    if (count > 0) {
      masks.push(locator);
    }
  }
  return masks;
}

/**
 * Wait for the page to reach a visually stable state.
 * Checks that the app root has mounted (header visible), waits for
 * any skeleton-to-loaded transition to complete, and pauses for
 * two animation frames to let the compositor settle.
 *
 * This function does NOT freeze the clock or kill animations; those
 * must be called separately (freezeClock before goto, killAnimations
 * after load).
 */
export async function waitForStable(page: Page): Promise<void> {
  // Wait for the Preact app to bootstrap (header mounts)
  await page.waitForSelector('header', { state: 'attached', timeout: 15000 });

  // Wait for skeleton to disappear if present (Pitfall 19)
  const skeleton = page.locator('[data-testid="sidebar-skeleton"]');
  const skeletonVisible = await skeleton.isVisible().catch(() => false);
  if (skeletonVisible) {
    await skeleton.waitFor({ state: 'detached', timeout: 10000 });
  }

  // Allow two animation frames for compositor to settle
  await page.evaluate(() => new Promise<void>((resolve) => {
    requestAnimationFrame(() => {
      requestAnimationFrame(() => resolve());
    });
  }));

  // Final stabilization pause (200ms covers any remaining async renders)
  await page.waitForTimeout(200);
}

/**
 * All-in-one preparation before taking a visual regression screenshot.
 * Combines killAnimations + waitForStable in the correct order.
 *
 * Call AFTER page.goto() has completed. freezeClock() must be called
 * BEFORE page.goto() separately.
 *
 * Usage:
 *   await freezeClock(page);        // before goto
 *   await mockEndpoints(page);      // before goto
 *   await page.goto('/?token=test');
 *   await prepareForScreenshot(page); // after goto
 *   await expect(page).toHaveScreenshot('name.png', {
 *     mask: await getDynamicContentMasks(page),
 *   });
 */
export async function prepareForScreenshot(page: Page): Promise<void> {
  await killAnimations(page);
  await waitForStable(page);
}

/** Standard fixture menu with groups and sessions across all statuses. */
export const FIXTURE_MENU = {
  items: [
    { type: 'group', level: 0, group: { path: 'work', name: 'Work', expanded: true, sessionCount: 2 } },
    { type: 'session', level: 1, session: { id: 's1', title: 'Build pipeline', status: 'running', tool: 'claude', groupPath: 'work' } },
    { type: 'session', level: 1, session: { id: 's2', title: 'Research docs', status: 'waiting', tool: 'shell', groupPath: 'work' } },
    { type: 'group', level: 0, group: { path: 'personal', name: 'Personal', expanded: true, sessionCount: 2 } },
    { type: 'session', level: 1, session: { id: 's3', title: 'Blog drafts', status: 'idle', tool: 'claude', groupPath: 'personal' } },
    { type: 'session', level: 1, session: { id: 's4', title: 'Errored task', status: 'error', tool: 'shell', groupPath: 'personal' } },
  ],
};

export const EMPTY_MENU = { items: [] };

export const FIXTURE_COSTS_SUMMARY = {
  today_usd: 12.34, today_events: 5,
  week_usd: 67.89, week_events: 42,
  month_usd: 234.56, month_events: 200,
  projected_usd: 500.00,
};

export const FIXTURE_COSTS_DAILY = [
  { date: '2026-01-01', cost_usd: 5.01 },
  { date: '2026-01-02', cost_usd: 7.12 },
  { date: '2026-01-03', cost_usd: 9.44 },
  { date: '2026-01-04', cost_usd: 3.33 },
  { date: '2026-01-05', cost_usd: 6.78 },
  { date: '2026-01-06', cost_usd: 8.01 },
  { date: '2026-01-07', cost_usd: 12.34 },
];

export const FIXTURE_COSTS_MODELS = {
  'claude-opus-4': 120.5,
  'claude-sonnet-4': 84.2,
  'gpt-4o': 30.0,
};

export const FIXTURE_PROFILES = {
  current: 'default',
  profiles: ['default', 'work', 'personal'],
};

export const FIXTURE_SETTINGS = { webMutations: true };

/**
 * Mock all API endpoints with deterministic fixture data.
 * Must be called BEFORE page.goto() because page.route()
 * must be installed before the page makes requests.
 */
export async function mockEndpoints(page: Page, opts: { menu?: any } = {}): Promise<void> {
  const menu = opts.menu || FIXTURE_MENU;
  await page.route('**/api/menu*', r => r.fulfill({ json: menu }));
  await page.route('**/api/costs/summary*', r => r.fulfill({ json: FIXTURE_COSTS_SUMMARY }));
  await page.route('**/api/costs/daily*', r => r.fulfill({ json: FIXTURE_COSTS_DAILY }));
  await page.route('**/api/costs/models*', r => r.fulfill({ json: FIXTURE_COSTS_MODELS }));
  await page.route('**/api/profiles*', r => r.fulfill({ json: FIXTURE_PROFILES }));
  await page.route('**/api/settings*', r => r.fulfill({ json: FIXTURE_SETTINGS }));
  await page.route('**/events/menu*', r => r.abort());
}
