// Main views — rewritten for the redesigned Preact shell (issue #1298).
//
// The old suite targeted the pre-redesign Tailwind UI (hamburger button,
// "Open info panel" button, Tailwind cost grid). The redesigned shell
// (internal/web/static/app/) boots into the Fleet tab, uses topbar tabs
// for navigation, opens Settings via the command palette, and replaces
// the mobile hamburger/sidebar-drawer with a bottom tab bar (.mob-tabs).
//
// Mapping from the legacy suite:
//   empty state            -> fleet empty state (Fleet pane is the new landing view)
//   sidebar with sessions  -> kept (new .sidebar markup)
//   cost dashboard         -> costs pane via the "Costs" topbar tab
//                             (Chart.js canvases masked — canvas AA is not
//                             bit-stable; stat cards + chrome stay protected)
//   mobile sidebar         -> DROPPED: no hamburger / mobile sidebar drawer
//                             exists anymore (sidebar is display:none <=720px,
//                             MobileTabs.js bottom bar is the mobile nav).
//                             Replaced by "mobile fleet with bottom tabs".
//   settings panel         -> settings drawer via command palette (the only
//                             entry point in the new shell, CommandPalette.js)
import { test, expect } from '@playwright/test';
import {
  freezeClock, mockEndpoints, prepareForScreenshot,
  getDynamicContentMasks, chartMasks, EMPTY_MENU,
} from './visual-helpers.js';

test.describe('Main views visual baselines', () => {
  test('fleet empty state — desktop dark 1280x800', async ({ page }) => {
    await freezeClock(page);
    await mockEndpoints(page, { menu: EMPTY_MENU });
    await page.goto('/?token=test');
    await prepareForScreenshot(page);
    // FleetPane.js renders the stat tiles and the "No sessions yet" hint
    await expect(page.locator('.fleet-stats')).toBeVisible();
    const masks = await getDynamicContentMasks(page);
    await expect(page).toHaveScreenshot('fleet-empty-dark-1280x800.png', { mask: masks });
  });

  test('sidebar with sessions — desktop dark 1280x800', async ({ page }) => {
    await freezeClock(page);
    await mockEndpoints(page);
    await page.goto('/?token=test');
    await prepareForScreenshot(page);
    // Sidebar.js renders one .sess row per fixture session
    await expect(page.locator('.sidebar .sess')).toHaveCount(4);
    const masks = await getDynamicContentMasks(page);
    await expect(page).toHaveScreenshot('sidebar-sessions-dark-1280x800.png', { mask: masks });
  });

  test('costs pane — desktop dark 1280x800', async ({ page }) => {
    await freezeClock(page);
    await mockEndpoints(page);
    await page.goto('/?token=test');
    await prepareForScreenshot(page);
    // Topbar.js: tab buttons are .top-tab with text labels
    await page.locator('.top-tab', { hasText: 'Costs' }).click();
    // CostDashboard.js: summary cards render in .stat-grid with "N events" deltas
    await page.waitForFunction(() => {
      const grid = document.querySelector('.costs .stat-grid');
      return !!(grid && grid.textContent && grid.textContent.includes('events'));
    }, { timeout: 10000 });
    // Let Chart.js finish its (masked) initial render
    await page.waitForSelector('.chart-card canvas', { state: 'attached', timeout: 10000 });
    await page.waitForTimeout(1000);
    await prepareForScreenshot(page);
    const masks = [...await getDynamicContentMasks(page), ...await chartMasks(page)];
    await expect(page).toHaveScreenshot('costs-pane-dark-1280x800.png', { mask: masks });
  });

  test('mobile fleet with bottom tabs — 375x812 dark', async ({ page }) => {
    await page.setViewportSize({ width: 375, height: 812 });
    await freezeClock(page);
    await mockEndpoints(page);
    await page.goto('/?token=test');
    await prepareForScreenshot(page);
    // MobileTabs.js: bottom bar replaces the old hamburger/sidebar drawer.
    // 5 tabs since #1462 added Command Center as the first mobile tab.
    await expect(page.locator('.mob-tabs')).toBeVisible();
    await expect(page.locator('.mob-tab')).toHaveCount(5);
    const masks = await getDynamicContentMasks(page);
    await expect(page).toHaveScreenshot('mobile-fleet-dark-375x812.png', { mask: masks });
  });

  test('settings drawer — desktop dark 1280x800', async ({ page }) => {
    await freezeClock(page);
    await mockEndpoints(page, { menu: EMPTY_MENU });
    await page.goto('/?token=test');
    await prepareForScreenshot(page);
    // The settings drawer opens via the command palette (CommandPalette.js
    // "Settings drawer" command -> infoDrawerOpenSignal in AppShell.js).
    await page.keyboard.press('Control+k');
    await page.waitForSelector('.cmdk', { state: 'visible', timeout: 5000 });
    await page.locator('.cmdk .row', { hasText: 'Settings drawer' }).click();
    // SettingsPanel.js renders .kv rows once /api/settings (mocked) resolves
    await page.waitForSelector('.dialog .kv', { state: 'visible', timeout: 5000 });
    await prepareForScreenshot(page);
    const masks = await getDynamicContentMasks(page);
    await expect(page).toHaveScreenshot('settings-drawer-dark-1280x800.png', { mask: masks });
  });
});
