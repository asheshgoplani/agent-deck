// P1 bug regression baselines — rewritten for the redesigned Preact shell
// (issue #1298).
//
// Mapping from the legacy suite:
//   WEB-P1-1 terminal fills container -> kept: selecting a session switches
//     to the Terminal tab and mounts .term-frame (TerminalPanel.js). The
//     frame INTERIOR is masked (xterm canvas + WS error banner depend on a
//     live tmux session the fixture server doesn't have); the mask still
//     pins the frame's position/size, which is the layout contract.
//   WEB-P1-2 fluid sidebar at 1920x1080 -> kept (whole-shell fluid layout).
//   WEB-P1-3 row density 40px -> reworked: density is now a design token
//     driven by TweaksPanel.js / uiState.js (localStorage agentdeck.density,
//     [data-density] in design-tokens.css). Compact density with a long
//     session list protects the same regression surface.
//   WEB-P1-4 empty state card grid 1920x1080 -> fleet empty state at
//     1920x1080 (the Fleet pane is the new empty-state landing surface).
//   WEB-P1-5 mobile overflow menu -> DROPPED: no overflow menu exists in the
//     new topbar/mobile layout. Replaced by the terminal-tab empty state
//     (EmptyStateDashboard.js, "No session selected"), which is the other
//     legacy empty-state concept that still exists.
import { test, expect } from '@playwright/test';
import {
  freezeClock, mockEndpoints, prepareForScreenshot,
  getDynamicContentMasks, EMPTY_MENU,
} from './visual-helpers.js';

test.describe('P1 bug regression baselines', () => {
  test('WEB-P1-1: terminal frame fills container at 1280x800', async ({ page }) => {
    await freezeClock(page);
    await mockEndpoints(page);
    await page.goto('/?token=test');
    await prepareForScreenshot(page);
    // Sidebar.js: clicking a .sess row selects it and activates the Terminal tab
    const row = page.locator('.sidebar .sess', { hasText: 'Build pipeline' });
    await row.waitFor({ state: 'visible', timeout: 5000 });
    await row.click();
    // TerminalPanel.js mounts .term-frame for the selected session
    await page.waitForSelector('.term-frame', { state: 'visible', timeout: 5000 });
    await page.waitForTimeout(500);
    await prepareForScreenshot(page);
    // Mask the frame interior: xterm/WebGL output and the #782 error banner
    // are driven by a real WS to a tmux session that doesn't exist in the
    // fixture server. The masked block still locks the frame geometry.
    const masks = [...await getDynamicContentMasks(page), page.locator('.term-frame')];
    await expect(page).toHaveScreenshot('terminal-fill-1280x800.png', { mask: masks });
  });

  test('WEB-P1-2: fluid layout at 1920x1080', async ({ page }) => {
    await page.setViewportSize({ width: 1920, height: 1080 });
    await freezeClock(page);
    await mockEndpoints(page);
    await page.goto('/?token=test');
    await prepareForScreenshot(page);
    const masks = await getDynamicContentMasks(page);
    await expect(page).toHaveScreenshot('fluid-layout-1920x1080.png', { mask: masks });
  });

  test('WEB-P1-3: compact density with many sessions at 1280x800', async ({ page }) => {
    // uiState.js reads JSON from localStorage key agentdeck.density before
    // the shell mounts; design-tokens.css [data-density="compact"] shrinks
    // --density-row/--density-gap.
    await page.addInitScript(() => {
      localStorage.setItem('agentdeck.density', JSON.stringify('compact'));
    });
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
    // Token wiring guard: the dataset attribute must reflect the stored value
    const density = await page.evaluate(() => document.documentElement.dataset.density);
    expect(density).toBe('compact');
    const masks = await getDynamicContentMasks(page);
    await expect(page).toHaveScreenshot('row-density-compact-1280x800.png', { mask: masks });
  });

  test('WEB-P1-4: fleet empty state at 1920x1080', async ({ page }) => {
    await page.setViewportSize({ width: 1920, height: 1080 });
    await freezeClock(page);
    await mockEndpoints(page, { menu: EMPTY_MENU });
    await page.goto('/?token=test');
    await prepareForScreenshot(page);
    await expect(page.locator('.fleet-stats')).toBeVisible();
    const masks = await getDynamicContentMasks(page);
    await expect(page).toHaveScreenshot('fleet-empty-1920x1080.png', { mask: masks });
  });

  test('WEB-P1-5: terminal empty state (no session selected) at 1280x800', async ({ page }) => {
    await freezeClock(page);
    await mockEndpoints(page);
    await page.goto('/?token=test');
    await prepareForScreenshot(page);
    // Switch to the Terminal tab without selecting a session:
    // TerminalPanel.js renders EmptyStateDashboard.js in that case.
    await page.locator('.top-tab', { hasText: 'Terminal' }).click();
    await page.waitForSelector('[data-testid="empty-state-dashboard"]', { state: 'visible', timeout: 5000 });
    await prepareForScreenshot(page);
    const masks = await getDynamicContentMasks(page);
    await expect(page).toHaveScreenshot('terminal-empty-state-1280x800.png', { mask: masks });
  });
});
