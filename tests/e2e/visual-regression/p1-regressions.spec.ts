import { test, expect } from '@playwright/test';
import {
  freezeClock, mockEndpoints, prepareForScreenshot,
  getDynamicContentMasks, EMPTY_MENU,
} from './visual-helpers.js';

test.describe('P1 bug regression baselines', () => {
  test('WEB-P1-1: terminal panel fills container at 1280x800', async ({ page }) => {
    await freezeClock(page);
    await mockEndpoints(page);
    await page.goto('/?token=test');
    await prepareForScreenshot(page);
    // Select a session to show the terminal panel area
    const sessionBtn = page.locator('#preact-session-list button[data-session-id="s1"]');
    await sessionBtn.waitFor({ state: 'visible', timeout: 5000 });
    await sessionBtn.click();
    await page.waitForTimeout(500);
    await prepareForScreenshot(page);
    const masks = await getDynamicContentMasks(page);
    await expect(page).toHaveScreenshot('terminal-fill-1280x800.png', { mask: masks });
  });

  test('WEB-P1-2: fluid sidebar at 1920x1080', async ({ page }) => {
    await page.setViewportSize({ width: 1920, height: 1080 });
    await freezeClock(page);
    await mockEndpoints(page);
    await page.goto('/?token=test');
    await prepareForScreenshot(page);
    const masks = await getDynamicContentMasks(page);
    await expect(page).toHaveScreenshot('fluid-sidebar-1920x1080.png', { mask: masks });
  });

  test('WEB-P1-3: row density 40px at 1280x800', async ({ page }) => {
    await freezeClock(page);
    // Use a menu with many sessions to show density
    await mockEndpoints(page, {
      menu: {
        items: [
          { type: 'group', level: 0, group: { path: 'work', name: 'Work', expanded: true, sessionCount: 5 } },
          { type: 'session', level: 1, session: { id: 's1', title: 'Session Alpha', status: 'running', tool: 'claude', groupPath: 'work' } },
          { type: 'session', level: 1, session: { id: 's2', title: 'Session Beta', status: 'waiting', tool: 'shell', groupPath: 'work' } },
          { type: 'session', level: 1, session: { id: 's3', title: 'Session Gamma', status: 'idle', tool: 'claude', groupPath: 'work' } },
          { type: 'session', level: 1, session: { id: 's4', title: 'Session Delta', status: 'error', tool: 'shell', groupPath: 'work' } },
          { type: 'session', level: 1, session: { id: 's5', title: 'Session Epsilon', status: 'running', tool: 'claude', groupPath: 'work' } },
          { type: 'group', level: 0, group: { path: 'personal', name: 'Personal', expanded: true, sessionCount: 4 } },
          { type: 'session', level: 1, session: { id: 's6', title: 'Session Zeta', status: 'idle', tool: 'claude', groupPath: 'personal' } },
          { type: 'session', level: 1, session: { id: 's7', title: 'Session Eta', status: 'waiting', tool: 'shell', groupPath: 'personal' } },
          { type: 'session', level: 1, session: { id: 's8', title: 'Session Theta', status: 'running', tool: 'claude', groupPath: 'personal' } },
          { type: 'session', level: 1, session: { id: 's9', title: 'Session Iota', status: 'error', tool: 'shell', groupPath: 'personal' } },
        ],
      },
    });
    await page.goto('/?token=test');
    await prepareForScreenshot(page);
    const masks = await getDynamicContentMasks(page);
    await expect(page).toHaveScreenshot('row-density-40px-1280x800.png', { mask: masks });
  });

  test('WEB-P1-4: empty state card grid at 1920x1080', async ({ page }) => {
    await page.setViewportSize({ width: 1920, height: 1080 });
    await freezeClock(page);
    await mockEndpoints(page, { menu: EMPTY_MENU });
    await page.goto('/?token=test');
    await prepareForScreenshot(page);
    const masks = await getDynamicContentMasks(page);
    await expect(page).toHaveScreenshot('empty-state-card-grid-1920x1080.png', { mask: masks });
  });

  test('WEB-P1-5: mobile overflow menu at 375x667', async ({ page }) => {
    await page.setViewportSize({ width: 375, height: 667 });
    await freezeClock(page);
    await mockEndpoints(page);
    await page.goto('/?token=test');
    await prepareForScreenshot(page);
    const masks = await getDynamicContentMasks(page);
    await expect(page).toHaveScreenshot('mobile-overflow-menu-375x667.png', { mask: masks });
  });
});
