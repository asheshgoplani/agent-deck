import { test, expect } from '@playwright/test';
import {
  freezeClock, mockEndpoints, prepareForScreenshot,
  getDynamicContentMasks, EMPTY_MENU,
} from './visual-helpers.js';

test.describe('Main views visual baselines', () => {
  test('empty state — desktop dark 1280x800', async ({ page }) => {
    await freezeClock(page);
    await mockEndpoints(page, { menu: EMPTY_MENU });
    await page.goto('/?token=test');
    await prepareForScreenshot(page);
    const masks = await getDynamicContentMasks(page);
    await expect(page).toHaveScreenshot('empty-state-dark-1280x800.png', { mask: masks });
  });

  test('sidebar with sessions — desktop dark 1280x800', async ({ page }) => {
    await freezeClock(page);
    await mockEndpoints(page);
    await page.goto('/?token=test');
    await prepareForScreenshot(page);
    const masks = await getDynamicContentMasks(page);
    await expect(page).toHaveScreenshot('sidebar-sessions-dark-1280x800.png', { mask: masks });
  });

  test('cost dashboard — desktop dark 1280x800', async ({ page }) => {
    await freezeClock(page);
    await mockEndpoints(page);
    await page.goto('/?token=test');
    await prepareForScreenshot(page);
    await page.locator('button[title="Cost Dashboard"]').click();
    await page.waitForFunction(
      () => {
        const grid = document.querySelector('.grid.grid-cols-2');
        return !!(grid && grid.textContent && grid.textContent.includes('events'));
      },
      { timeout: 10000 },
    );
    // Re-stabilize after tab switch
    await prepareForScreenshot(page);
    const masks = await getDynamicContentMasks(page);
    await expect(page).toHaveScreenshot('cost-dashboard-dark-1280x800.png', { mask: masks });
  });

  test('mobile sidebar — 375x812 dark', async ({ page }) => {
    await page.setViewportSize({ width: 375, height: 812 });
    await freezeClock(page);
    await mockEndpoints(page);
    await page.goto('/?token=test');
    await prepareForScreenshot(page);
    // Open mobile sidebar via hamburger — the actual aria-label is "Open sidebar"
    const hamburger = page.locator('button[aria-label="Open sidebar"]');
    await hamburger.waitFor({ state: 'visible', timeout: 5000 });
    await hamburger.click();
    // Wait for sidebar drawer to animate open
    await page.waitForTimeout(300);
    await prepareForScreenshot(page);
    const masks = await getDynamicContentMasks(page);
    await expect(page).toHaveScreenshot('mobile-sidebar-dark-375x812.png', { mask: masks });
  });

  test('settings panel — desktop dark 1280x800', async ({ page }) => {
    await freezeClock(page);
    await mockEndpoints(page, { menu: EMPTY_MENU });
    await page.goto('/?token=test');
    await prepareForScreenshot(page);
    // Open the info panel which contains the settings — aria-label is "Open info panel"
    const infoBtn = page.locator('button[aria-label="Open info panel"]');
    await infoBtn.waitFor({ state: 'visible', timeout: 5000 });
    await infoBtn.click();
    // Wait for settings panel to render
    await page.waitForTimeout(300);
    await prepareForScreenshot(page);
    const masks = await getDynamicContentMasks(page);
    await expect(page).toHaveScreenshot('settings-panel-dark-1280x800.png', { mask: masks });
  });
});
