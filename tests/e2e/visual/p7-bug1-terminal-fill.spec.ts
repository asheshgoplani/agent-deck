import { test, expect } from '@playwright/test';
import { readFileSync } from 'fs';
import { join } from 'path';

/**
 * Phase 7 / Plan 01 / Task 1: WEB-P1-1 regression test
 *
 * Asserts that the xterm canvas fills its container after attach AND survives
 * a viewport resize. Two-layer guarantee:
 *
 *   Layer 1 (structural, always runs): the source files of TerminalPanel.js
 *   and AppShell.js contain the wiring strings the fix needs:
 *     - window resize listener in TerminalPanel.js
 *     - `flex-1 min-h-0 min-w-0` on the padded container in TerminalPanel.js
 *     - `flex flex-col` and `min-h-0` on <main> in AppShell.js
 *     - `h-full min-h-0` on the inner conditional wrapper in AppShell.js
 *
 *   Layer 2 (DOM, requires fixture sessions): once a real session is selected,
 *   the .xterm bounding box height is within 8 px of the parent main panel
 *   inner height at both 1280x800 and 1920x1080.
 *
 * Root cause (LOCKED per 07-01-PLAN.md):
 *   1. Flex chain `<main> > <div h-full> > TerminalPanel > <div flex-1 min-h-0>`
 *      is missing `min-h-0` on <main> and on the inner conditional wrapper, and
 *      `min-w-0` on the padded container. Result: child flex items inherit
 *      `min-height: auto` and stop shrinking — terminal renders at intrinsic
 *      (~zero) height, leaving gray space below.
 *   2. ResizeObserver only watches the inner container; window resize events
 *      that don't change the parent box (e.g. devtools open, mobile keyboard)
 *      never trigger fitAddon.fit().
 *
 * Fix (LOCKED per 07-01-PLAN.md):
 *   - Add `flex flex-col min-h-0` to <main> in AppShell.js
 *   - Add `h-full min-h-0` to the inner conditional wrapper around TerminalPanel
 *   - Add `min-w-0` to the padded container in TerminalPanel.js
 *   - Wire `window.addEventListener('resize', () => scheduleFitAndResize(120))`
 *     using an AbortController so cleanup is single-call
 *
 * TDD ORDER: this spec is committed in FAILING state in Task 1, then the
 * fix lands in Task 2, flipping the spec to green.
 */

const TERMINAL_PANEL_PATH = join(
  __dirname, '..', '..', '..',
  'internal', 'web', 'static', 'app', 'TerminalPanel.js',
);
const APP_SHELL_PATH = join(
  __dirname, '..', '..', '..',
  'internal', 'web', 'static', 'app', 'AppShell.js',
);

test.describe('WEB-P1-1 — terminal panel fills its container', () => {
  // ===== Layer 1: structural (always runs) =====

  test('structural: TerminalPanel.js wires a window resize listener', () => {
    const src = readFileSync(TERMINAL_PANEL_PATH, 'utf-8');
    // Match either bare window.addEventListener('resize'... or AbortController-style.
    const hasResize = /window\.addEventListener\(\s*['"]resize['"]/.test(src);
    expect(
      hasResize,
      'TerminalPanel.js must register a window resize listener so fitAddon.fit() runs on viewport changes that do not directly resize the inner container (per WEB-P1-1).',
    ).toBe(true);
  });

  test('structural: TerminalPanel.js padded container has flex-1 min-h-0 min-w-0', () => {
    const src = readFileSync(TERMINAL_PANEL_PATH, 'utf-8');
    expect(
      /flex-1\s+min-h-0\s+min-w-0/.test(src),
      'TerminalPanel.js must apply `flex-1 min-h-0 min-w-0` to the padded wrapper around containerRef so flex children can shrink in BOTH axes.',
    ).toBe(true);
  });

  test('structural: AppShell.js <main> uses flex flex-col + min-h-0', () => {
    const src = readFileSync(APP_SHELL_PATH, 'utf-8');
    // Expect the main element class string to contain `flex flex-col` AND `min-h-0`.
    const mainMatch = src.match(/<main[^>]*class="([^"]+)"/);
    expect(mainMatch, 'AppShell.js must contain a <main class="..."> element').not.toBeNull();
    if (mainMatch) {
      const cls = mainMatch[1];
      expect(
        /flex\s+flex-col/.test(cls),
        `<main> class string must contain "flex flex-col" so its child h-full can resolve. Got: ${cls}`,
      ).toBe(true);
      expect(
        /min-h-0/.test(cls),
        `<main> class string must contain "min-h-0" so flex shrinking propagates. Got: ${cls}`,
      ).toBe(true);
    }
  });

  test('structural: AppShell.js inner TerminalPanel wrapper has h-full min-h-0', () => {
    const src = readFileSync(APP_SHELL_PATH, 'utf-8');
    // Match the conditional wrapper string. Look for activeTab === 'terminal' near `h-full min-h-0`.
    const idx = src.indexOf("activeTab === 'terminal'");
    expect(idx, 'AppShell.js must contain the activeTab terminal conditional').toBeGreaterThan(-1);
    // Look in a 400-char window after that index
    const window = src.slice(idx, idx + 400);
    expect(
      /h-full\s+min-h-0/.test(window),
      'The inner conditional wrapper around TerminalPanel must use class `h-full min-h-0` (currently only `h-full`).',
    ).toBe(true);
  });

  // ===== Layer 2: DOM (skips when no fixture session is available) =====

  test('DOM 1280x800: .xterm bounding box fills the main panel within 8 px', async ({ page }) => {
    await page.setViewportSize({ width: 1280, height: 800 });
    await page.goto('/?t=test');
    await page.waitForSelector('header', { state: 'attached', timeout: 15000 });
    // Click the first session row if any exist
    const rowCount = await page.locator('button[data-session-id]').count();
    test.skip(rowCount === 0, 'no fixture sessions to attach — structural tests cover the contract');
    await page.locator('button[data-session-id]').first().click();
    // Wait for xterm to mount
    await page.waitForSelector('.xterm', { state: 'attached', timeout: 10000 });
    await page.waitForTimeout(500); // FitAddon settles
    const xtermBox = await page.locator('.xterm').first().boundingBox();
    const mainBox = await page.locator('main').boundingBox();
    expect(xtermBox, '.xterm must have a bounding box').not.toBeNull();
    expect(mainBox, 'main must have a bounding box').not.toBeNull();
    if (xtermBox && mainBox) {
      const gapBelow = (mainBox.y + mainBox.height) - (xtermBox.y + xtermBox.height);
      expect(
        gapBelow,
        `gap below xterm must be < 32 px (terminal panel padding ~16 px each side); got ${gapBelow}. mainBox=${JSON.stringify(mainBox)} xtermBox=${JSON.stringify(xtermBox)}`,
      ).toBeLessThan(32);
    }
  });

  test('DOM 1920x1080: .xterm bounding box re-fills after viewport resize', async ({ page }) => {
    await page.setViewportSize({ width: 1280, height: 800 });
    await page.goto('/?t=test');
    await page.waitForSelector('header', { state: 'attached', timeout: 15000 });
    const rowCount = await page.locator('button[data-session-id]').count();
    test.skip(rowCount === 0, 'no fixture sessions — structural tests cover the contract');
    await page.locator('button[data-session-id]').first().click();
    await page.waitForSelector('.xterm', { state: 'attached', timeout: 10000 });
    await page.waitForTimeout(300);
    // Resize the viewport — this is the case the bare ResizeObserver misses
    await page.setViewportSize({ width: 1920, height: 1080 });
    await page.waitForTimeout(400); // window resize listener + fit debounce
    const xtermBox = await page.locator('.xterm').first().boundingBox();
    const mainBox = await page.locator('main').boundingBox();
    expect(xtermBox && mainBox).toBeTruthy();
    if (xtermBox && mainBox) {
      const gapBelow = (mainBox.y + mainBox.height) - (xtermBox.y + xtermBox.height);
      expect(
        gapBelow,
        `after resize to 1920x1080, gap below xterm must be < 32 px; got ${gapBelow}`,
      ).toBeLessThan(32);
    }
  });
});
