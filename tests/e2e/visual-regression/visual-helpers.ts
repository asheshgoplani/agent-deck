import type { Page, Locator } from '@playwright/test';

/**
 * Helpers for the visual regression suite, rewritten for the redesigned
 * Preact shell (internal/web/static/app/): five-zone grid layout
 * (.topbar / .sidebar / .main / .rightrail / .footer), topbar tabs
 * (Fleet/Terminal/MCPs/Skills/Conductor/Watchers/Costs/Search), and the
 * mobile bottom tab bar (.mob-tabs, <=720px).
 *
 * Every selector in this file is grounded in the component source:
 *   Topbar.js, Sidebar.js, Footer.js, MobileTabs.js, AppShell.js,
 *   CommandPalette.js, TweaksPanel.js, SettingsPanel.js, Toast.js,
 *   panes/FleetPane.js, panes/CostsPane.js, CostDashboard.js,
 *   TerminalPanel.js, EmptyStateDashboard.js.
 */

/**
 * CSS injected to kill ALL animations and transitions instantly.
 * Applied via page.addStyleTag() before every screenshot.
 * NOTE: this does not stop <canvas> animations (Chart.js); tests that
 * show charts mask the canvases instead (see chartMasks()).
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
 * Must be called BEFORE any screenshot capture.
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
 * Locators for dynamic content that should be masked in screenshots.
 *
 * The redesigned shell renders very little non-deterministic content once
 * all API endpoints are mocked (mockEndpoints) and the clock is frozen
 * (freezeClock). The only remaining candidates are <time> elements (none
 * in the current bundle, kept defensively). Chart.js canvases are masked
 * per-test where charts appear via chartMasks().
 *
 * Only returns locators for elements that actually exist on the page.
 */
export async function getDynamicContentMasks(page: Page): Promise<Locator[]> {
  const selectors = [
    'time',
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
 * Chart.js renders into <canvas>, which killAnimations() cannot freeze
 * (canvas animation is driven by JS, not CSS) and whose anti-aliasing is
 * not bit-stable across runs. Tests that show the Costs dashboard mask
 * the canvases; the stat cards, titles, and layout chrome around them
 * stay unmasked and protected.
 */
export async function chartMasks(page: Page): Promise<Locator[]> {
  const masks: Locator[] = [];
  const canvases = page.locator('.chart-card canvas');
  if (await canvases.count() > 0) masks.push(canvases);
  return masks;
}

/**
 * Wait for the page to reach a visually stable state.
 *
 * - Topbar mounted (<header class="topbar">, Topbar.js).
 * - Profile <select> rendered (Topbar.js renders it only after
 *   /api/profiles resolves — mocked, so it always arrives). On phones it
 *   is display:none but still attached.
 * - Connection pill settled on "disconnected" (mockEndpoints aborts
 *   /events/menu, and connectionSignal only ever moves from "connecting"
 *   to "disconnected" in that setup — waiting removes the race between
 *   the two states).
 * - Web fonts loaded (index.html pulls Inter/JetBrains Mono from Google
 *   Fonts; capturing before fonts swap in causes whole-page diffs).
 */
export async function waitForStable(page: Page): Promise<void> {
  // Preact shell bootstrapped (Topbar.js renders <header class="topbar">)
  await page.waitForSelector('header.topbar', { state: 'attached', timeout: 15000 });

  // Profile dropdown present => /api/profiles fixture applied (Topbar.js)
  await page.waitForSelector('.top-right select', { state: 'attached', timeout: 15000 });

  // SSE is aborted by mockEndpoints, so the pill deterministically ends
  // on "ws · disconnected". Wait for it so we never capture "connecting".
  await page.waitForFunction(
    () => {
      const el = document.querySelector('.conn-pill');
      return !!el && /disconnected/.test(el.textContent || '');
    },
    { timeout: 15000 },
  );

  // Web fonts (prevents fallback-font frames in the screenshot)
  await page.evaluate(() => (document as any).fonts?.ready);

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
 * Call AFTER page.goto() has completed. freezeClock() and mockEndpoints()
 * must be called BEFORE page.goto() separately.
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

/**
 * Fully populated so SettingsPanel.js renders every .kv row
 * deterministically (no "unknown" version, no "loading…" picker tools).
 */
export const FIXTURE_SETTINGS = {
  profile: '_test',
  version: 'v0.0.0-visualtest',
  readOnly: false,
  webMutations: true,
  hiddenTools: [],
  pickerTools: ['claude', 'shell'],
};

/**
 * Empty stats: Footer.js only renders the cpu/mem segments when the
 * corresponding blocks are present, so {} deterministically hides them
 * (real values change every poll and would diff every run).
 */
export const FIXTURE_SYSTEM_STATS = {};

/**
 * Mock all API endpoints with deterministic fixture data.
 * Must be called BEFORE page.goto() because page.route()
 * must be installed before the page makes requests.
 *
 * Endpoints covered (everything the app calls on boot or per-pane):
 *   /api/menu, /api/costs/{summary,daily,models}, /api/profiles,
 *   /api/settings, /api/system/stats, and the /events/menu SSE stream
 *   (aborted so connection state settles on "disconnected").
 */
export async function mockEndpoints(page: Page, opts: { menu?: any } = {}): Promise<void> {
  const menu = opts.menu || FIXTURE_MENU;
  await page.route('**/api/menu*', r => r.fulfill({ json: menu }));
  await page.route('**/api/costs/summary*', r => r.fulfill({ json: FIXTURE_COSTS_SUMMARY }));
  await page.route('**/api/costs/daily*', r => r.fulfill({ json: FIXTURE_COSTS_DAILY }));
  await page.route('**/api/costs/models*', r => r.fulfill({ json: FIXTURE_COSTS_MODELS }));
  await page.route('**/api/profiles*', r => r.fulfill({ json: FIXTURE_PROFILES }));
  await page.route('**/api/settings*', r => r.fulfill({ json: FIXTURE_SETTINGS }));
  await page.route('**/api/system/stats*', r => r.fulfill({ json: FIXTURE_SYSTEM_STATS }));
  await page.route('**/events/menu*', r => r.abort());
}
