// Polish regression baselines — rewritten for the redesigned Preact shell
// (issue #1298).
//
// Mapping from the legacy suite:
//   POL-1 skeleton loading + skeleton-to-loaded -> DROPPED: the redesigned
//     Sidebar.js has no skeleton state. sessionsLoadedSignal still exists in
//     state.js but no component consumes it, and no skeleton markup/class
//     exists in app.css. There is nothing left to protect.
//   POL-4 group density tight -> kept (multiple expanded groups in the new
//     .side-list markup).
//   POL-6 light theme sidebar -> DROPPED: the redesigned shell is dark-only.
//     design-tokens.css defines a single (Tokyo Night) palette with no light
//     variant, and no theme toggle exists in the new UI. The "visual theming"
//     protective intent is kept via the accent-token test below, plus a
//     baseline for the TweaksPanel itself (the new theming surface).
import { test, expect } from '@playwright/test';
import {
  freezeClock, mockEndpoints, prepareForScreenshot,
  getDynamicContentMasks,
} from './visual-helpers.js';

test.describe('Polish regression baselines', () => {
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
    await expect(page.locator('.sidebar .side-group-head')).toHaveCount(3);
    const masks = await getDynamicContentMasks(page);
    await expect(page).toHaveScreenshot('group-density-tight-1280x800.png', { mask: masks });
  });

  test('POL-6: green accent tokens at 1280x800', async ({ page }) => {
    // Replaces the light-theme test: accent swap via design tokens is the
    // theming mechanism of the redesigned shell. uiState.js reads JSON from
    // localStorage agentdeck.accent; [data-accent="green"] swaps --accent.
    await page.addInitScript(() => {
      localStorage.setItem('agentdeck.accent', JSON.stringify('green'));
    });
    await freezeClock(page);
    await mockEndpoints(page);
    await page.goto('/?token=test');
    await prepareForScreenshot(page);
    const accent = await page.evaluate(() => document.documentElement.dataset.accent);
    expect(accent).toBe('green');
    const masks = await getDynamicContentMasks(page);
    await expect(page).toHaveScreenshot('accent-green-1280x800.png', { mask: masks });
  });

  test('POL-7: tweaks panel open at 1280x800', async ({ page }) => {
    await freezeClock(page);
    await mockEndpoints(page);
    await page.goto('/?token=test');
    await prepareForScreenshot(page);
    // Topbar.js: the Tweaks toggle is the icon button with aria-label="Tweaks"
    await page.locator('button[aria-label="Tweaks"]').click();
    // TweaksPanel.js renders role="dialog" aria-label="Tweaks" with .tweaks class
    await page.waitForSelector('.tweaks', { state: 'visible', timeout: 5000 });
    await prepareForScreenshot(page);
    const masks = await getDynamicContentMasks(page);
    await expect(page).toHaveScreenshot('tweaks-panel-1280x800.png', { mask: masks });
  });
});
