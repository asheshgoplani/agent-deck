// P0 bug regression baselines — rewritten for the redesigned Preact shell
// (issue #1298).
//
// Mapping from the legacy suite:
//   WEB-P0-1 hamburger clickable -> DROPPED as written: the hamburger button
//     (aria-label="Open sidebar") no longer exists. The mobile nav is the
//     bottom tab bar (MobileTabs.js). The protective intent (mobile nav is
//     reachable and clickable) is kept: tap the "Costs" bottom tab and
//     verify the pane switches.
//   WEB-P0-2 profile switcher  -> kept: the topbar now renders a <select>
//     populated from /api/profiles (Topbar.js).
//   WEB-P0-3 titles not truncated -> kept (new .sess/.tt markup).
//   WEB-P0-4 toast stack capped at 3 -> kept: Toast.js preserves the cap-3
//     contract verbatim; triggered via failing DELETEs through the new
//     sidebar row actions + ConfirmDialog.
import { test, expect } from '@playwright/test';
import {
  freezeClock, mockEndpoints, prepareForScreenshot,
  getDynamicContentMasks, chartMasks,
} from './visual-helpers.js';

test.describe('P0 bug regression baselines', () => {
  test('WEB-P0-1: mobile bottom-tab nav to costs at 375x667', async ({ page }) => {
    await page.setViewportSize({ width: 375, height: 667 });
    await freezeClock(page);
    await mockEndpoints(page);
    await page.goto('/?token=test');
    await prepareForScreenshot(page);
    // MobileTabs.js bottom bar is the mobile navigation now
    const costsTab = page.locator('.mob-tab', { hasText: 'Costs' });
    await costsTab.waitFor({ state: 'visible', timeout: 5000 });
    await costsTab.click();
    // CostDashboard.js stat cards appear in the costs pane
    await page.waitForFunction(() => {
      const grid = document.querySelector('.costs .stat-grid');
      return !!(grid && grid.textContent && grid.textContent.includes('events'));
    }, { timeout: 10000 });
    await page.waitForTimeout(1000);
    await prepareForScreenshot(page);
    const masks = [...await getDynamicContentMasks(page), ...await chartMasks(page)];
    await expect(page).toHaveScreenshot('mobile-tab-nav-costs-375x667.png', { mask: masks });
  });

  test('WEB-P0-2: profile select populated at 1280x800', async ({ page }) => {
    await freezeClock(page);
    await mockEndpoints(page);
    await page.goto('/?token=test');
    await prepareForScreenshot(page);
    // Topbar.js renders the profile <select> only after /api/profiles resolves
    const select = page.locator('.top-right select');
    await expect(select).toBeVisible();
    await expect(select.locator('option')).toHaveCount(3);
    const masks = await getDynamicContentMasks(page);
    await expect(page).toHaveScreenshot('profile-select-1280x800.png', { mask: masks });
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
    await expect(page.locator('.sidebar .sess')).toHaveCount(3);
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

    // Trigger 5 error toasts to verify the cap at 3. Sidebar.js row actions
    // (.sess .actions) are revealed on hover; Delete opens ConfirmDialog.js
    // whose confirm button is .dialog .btn.danger ("Delete").
    const row = page.locator('.sidebar .sess', { hasText: 'Build pipeline' });
    for (let i = 0; i < 5; i++) {
      await row.hover();
      await row.locator('button.mini.danger[title="Delete"]').click();
      const confirmBtn = page.locator('.dialog .btn.danger', { hasText: 'Delete' });
      await confirmBtn.waitFor({ state: 'visible', timeout: 5000 });
      await confirmBtn.click();
      // apiFetch surfaces the DELETE failure as an error toast
      await expect(page.locator('.toast')).toHaveCount(Math.min(i + 1, 3));
    }

    // Functional guard for the visual check: never more than 3 visible
    await expect(page.locator('.toast')).toHaveCount(3);
    await page.waitForTimeout(300);
    await prepareForScreenshot(page);
    const masks = await getDynamicContentMasks(page);
    await expect(page).toHaveScreenshot('toast-cap-3-1280x800.png', { mask: masks });
  });
});
