import { test, expect } from '@playwright/test';
import {
  freezeClock, mockEndpoints, prepareForScreenshot,
  getDynamicContentMasks, killAnimations,
  FIXTURE_COSTS_SUMMARY, FIXTURE_COSTS_DAILY,
  FIXTURE_COSTS_MODELS, FIXTURE_PROFILES, FIXTURE_SETTINGS,
} from './visual-helpers.js';

test.describe('Polish regression baselines', () => {
  test('POL-1: skeleton loading state at 1280x800', async ({ page }) => {
    await freezeClock(page);
    // Mock all endpoints EXCEPT menu and SSE: the sidebar will show skeleton
    await page.route('**/api/costs/summary*', r => r.fulfill({ json: FIXTURE_COSTS_SUMMARY }));
    await page.route('**/api/costs/daily*', r => r.fulfill({ json: FIXTURE_COSTS_DAILY }));
    await page.route('**/api/costs/models*', r => r.fulfill({ json: FIXTURE_COSTS_MODELS }));
    await page.route('**/api/profiles*', r => r.fulfill({ json: FIXTURE_PROFILES }));
    await page.route('**/api/settings*', r => r.fulfill({ json: FIXTURE_SETTINGS }));
    await page.route('**/events/menu*', r => r.abort());
    // Delay the menu response so the skeleton is visible
    await page.route('**/api/menu*', () => {
      // Never fulfill: keep the skeleton visible
      return new Promise(() => {}); // hang forever
    });
    await page.goto('/?token=test');
    // Wait for header to mount (app bootstrap)
    await page.waitForSelector('header', { state: 'attached', timeout: 15000 });
    await killAnimations(page);
    await page.waitForTimeout(200);
    const masks = await getDynamicContentMasks(page);
    await expect(page).toHaveScreenshot('skeleton-loading-1280x800.png', { mask: masks });
  });

  test('POL-1: skeleton-to-loaded transition at 1280x800', async ({ page }) => {
    await freezeClock(page);
    await mockEndpoints(page);
    await page.goto('/?token=test');
    await prepareForScreenshot(page);
    // Verify skeleton is gone and real content is showing
    await page.waitForSelector('#preact-session-list', { state: 'attached', timeout: 10000 });
    const masks = await getDynamicContentMasks(page);
    await expect(page).toHaveScreenshot('skeleton-to-loaded-1280x800.png', { mask: masks });
  });

  test('POL-4: group density tight at 1280x800', async ({ page }) => {
    await freezeClock(page);
    // Use menu with multiple groups to verify tight group spacing
    await mockEndpoints(page, {
      menu: {
        items: [
          { type: 'group', level: 0, group: { path: 'work', name: 'Work', expanded: true, sessionCount: 1 } },
          { type: 'session', level: 1, session: { id: 's1', title: 'Build pipeline', status: 'running', tool: 'claude', groupPath: 'work' } },
          { type: 'group', level: 0, group: { path: 'personal', name: 'Personal', expanded: true, sessionCount: 1 } },
          { type: 'session', level: 1, session: { id: 's2', title: 'Blog drafts', status: 'idle', tool: 'claude', groupPath: 'personal' } },
          { type: 'group', level: 0, group: { path: 'research', name: 'Research', expanded: true, sessionCount: 1 } },
          { type: 'session', level: 1, session: { id: 's3', title: 'Paper review', status: 'waiting', tool: 'shell', groupPath: 'research' } },
        ],
      },
    });
    await page.goto('/?token=test');
    await prepareForScreenshot(page);
    const masks = await getDynamicContentMasks(page);
    await expect(page).toHaveScreenshot('group-density-tight-1280x800.png', { mask: masks });
  });

  test('POL-6: light theme sidebar at 1280x800', async ({ page }) => {
    // Force light theme via localStorage before SPA bootstraps
    await page.addInitScript(() => {
      localStorage.setItem('theme', 'light');
    });
    await freezeClock(page);
    await mockEndpoints(page);
    await page.goto('/?token=test');
    await prepareForScreenshot(page);
    // Verify light theme is active
    const isDark = await page.evaluate(() =>
      document.documentElement.classList.contains('dark'),
    );
    expect(isDark).toBe(false);
    const masks = await getDynamicContentMasks(page);
    await expect(page).toHaveScreenshot('light-theme-sidebar-1280x800.png', { mask: masks });
  });
});
