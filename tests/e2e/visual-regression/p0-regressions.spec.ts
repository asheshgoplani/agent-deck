import { test, expect } from '@playwright/test';
import {
  freezeClock, mockEndpoints, prepareForScreenshot,
  getDynamicContentMasks,
} from './visual-helpers.js';

test.describe('P0 bug regression baselines', () => {
  test('WEB-P0-1: hamburger clickable at 375x667', async ({ page }) => {
    await page.setViewportSize({ width: 375, height: 667 });
    await freezeClock(page);
    await mockEndpoints(page);
    await page.goto('/?token=test');
    await prepareForScreenshot(page);
    // aria-label is "Open sidebar" (or "Close sidebar") per Topbar.js
    const hamburger = page.locator('button[aria-label="Open sidebar"]');
    await hamburger.waitFor({ state: 'visible', timeout: 5000 });
    await hamburger.click();
    await page.waitForTimeout(300);
    await prepareForScreenshot(page);
    const masks = await getDynamicContentMasks(page);
    await expect(page).toHaveScreenshot('hamburger-clickable-375x667.png', { mask: masks });
  });

  test('WEB-P0-2: profile switcher readonly at 1280x800', async ({ page }) => {
    await freezeClock(page);
    await mockEndpoints(page);
    await page.goto('/?token=test');
    await prepareForScreenshot(page);
    const masks = await getDynamicContentMasks(page);
    // Screenshot the full page to capture the profile indicator in context
    await expect(page).toHaveScreenshot('profile-switcher-readonly-1280x800.png', { mask: masks });
  });

  test('WEB-P0-3: titles not truncated at 1280x800', async ({ page }) => {
    await freezeClock(page);
    // Use sessions with moderately long but non-truncating titles
    await mockEndpoints(page, {
      menu: {
        items: [
          { type: 'group', level: 0, group: { path: 'work', name: 'Engineering Work', expanded: true, sessionCount: 3 } },
          { type: 'session', level: 1, session: { id: 's1', title: 'Build pipeline setup and config', status: 'running', tool: 'claude', groupPath: 'work' } },
          { type: 'session', level: 1, session: { id: 's2', title: 'Database migration scripts', status: 'waiting', tool: 'shell', groupPath: 'work' } },
          { type: 'session', level: 1, session: { id: 's3', title: 'API endpoint refactoring', status: 'idle', tool: 'claude', groupPath: 'work' } },
        ],
      },
    });
    await page.goto('/?token=test');
    await prepareForScreenshot(page);
    const masks = await getDynamicContentMasks(page);
    await expect(page).toHaveScreenshot('title-no-truncation-1280x800.png', { mask: masks });
  });

  test('WEB-P0-4: toast stack capped at 3 visible', async ({ page }) => {
    await freezeClock(page);
    await mockEndpoints(page);
    // Make session delete API return 500 to trigger error toasts
    await page.route('**/api/sessions/*', r => {
      if (r.request().method() === 'DELETE') {
        return r.fulfill({ status: 500, json: { error: { message: 'Simulated failure' } } });
      }
      return r.fallback();
    });
    await page.goto('/?token=test');
    await prepareForScreenshot(page);

    // Trigger multiple toasts by attempting to delete sessions
    // We need to trigger 5 error toasts to verify the cap at 3
    for (let i = 0; i < 5; i++) {
      const row = page.locator('#preact-session-list button[data-session-id="s1"]');
      if (await row.isVisible()) {
        await row.hover();
        const deleteBtn = row.locator('button[aria-label="Delete session"]');
        if (await deleteBtn.isVisible()) {
          await deleteBtn.click();
          // Confirm the dialog if it appears
          const dialog = page.locator('.fixed.inset-0.z-50.bg-black\\/50');
          if (await dialog.isVisible().catch(() => false)) {
            await dialog.getByRole('button', { name: 'Delete', exact: true }).click();
            await page.waitForTimeout(200);
          }
        }
      }
    }

    await page.waitForTimeout(500);
    await prepareForScreenshot(page);
    const masks = await getDynamicContentMasks(page);
    await expect(page).toHaveScreenshot('toast-cap-3-1280x800.png', { mask: masks });
  });
});
