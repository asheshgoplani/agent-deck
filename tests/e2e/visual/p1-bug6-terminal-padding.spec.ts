import { test, expect } from '@playwright/test';
import { readFileSync } from 'fs';
import { join } from 'path';

/**
 * Phase 3 / Plan 02 / Task 1: BUG #6 / LAYT-03 regression test
 *
 * Asserts that the xterm host has 16px padding on all four edges via a
 * padding wrapper div, NOT directly on the xterm container (that would
 * break fitAddon.fit() cell-count math).
 *
 * Root cause (LOCKED per 03-CONTEXT.md): TerminalPanel.js line 318 renders
 *   <div ref=${containerRef} class="flex-1 min-h-0 overflow-hidden" />
 * with zero padding. xterm fills edge to edge.
 *
 * Fix (LOCKED per 03-CONTEXT.md): wrap the ref=${containerRef} div in a
 * padding wrapper:
 *   <div class="flex-1 min-h-0 p-sp-16 overflow-hidden">
 *     <div ref=${containerRef} class="h-full w-full overflow-hidden" />
 *   </div>
 *
 * Keep the mobile READ-ONLY banner above the wrapper untouched. Keep the
 * empty-state branch (EmptyStateDashboard when !sessionId) untouched.
 *
 * TDD ORDER: this spec is committed in FAILING state in Task 1, then the
 * fix lands in Task 2, flipping the spec to green.
 *
 * STRUCTURAL FALLBACK: if no fixture session can be opened, the runtime
 * padding-measurement tests skip. The file-read structural tests ALWAYS
 * run and provide the failing-before-fix guarantee.
 */

const TERMINAL_PANEL_PATH = join(
  __dirname, '..', '..', '..', 'internal', 'web', 'static', 'app', 'TerminalPanel.js',
);

function readTerminalPanelSrc(): string {
  return readFileSync(TERMINAL_PANEL_PATH, 'utf-8');
}

test.describe('BUG #6 / LAYT-03 — terminal panel has 16px padding on all edges', () => {
  // STRUCTURAL — always runs, fails before fix, passes after.
  test('structural: TerminalPanel.js has a p-sp-16 wrapper around ref=${containerRef}', () => {
    const src = readTerminalPanelSrc();
    // Wrapper with p-sp-16 contains the ref=${containerRef} div on a nearby line.
    const wrapperRe = /class="flex-1 min-h-0 p-sp-16 overflow-hidden"[\s\S]{0,200}ref=\$\{containerRef\}/;
    expect(
      wrapperRe.test(src),
      'TerminalPanel.js must wrap ref=${containerRef} in a <div class="flex-1 min-h-0 p-sp-16 overflow-hidden"> wrapper. See 03-CONTEXT.md LAYT-03 for the exact JSX block.',
    ).toBe(true);
  });

  test('structural: the pre-fix unwrapped form is absent', () => {
    const src = readTerminalPanelSrc();
    const unwrappedRe = /ref=\$\{containerRef\} class="flex-1 min-h-0 overflow-hidden"/;
    expect(
      unwrappedRe.test(src),
      'TerminalPanel.js still has the pre-fix unwrapped xterm host. Expected the ref=${containerRef} div to live inside a p-sp-16 wrapper with class="h-full w-full overflow-hidden".',
    ).toBe(false);
  });

  test('structural: inner containerRef div has h-full w-full classes', () => {
    const src = readTerminalPanelSrc();
    const innerRe = /ref=\$\{containerRef\} class="h-full w-full overflow-hidden"/;
    expect(
      innerRe.test(src),
      'TerminalPanel.js inner ref=${containerRef} div must have class="h-full w-full overflow-hidden" so it fills the padded wrapper.',
    ).toBe(true);
  });

  test('structural: empty-state branch EmptyStateDashboard is untouched', () => {
    const src = readTerminalPanelSrc();
    const emptyStateRe = /return html`<\$\{EmptyStateDashboard\} \/>`/;
    expect(
      emptyStateRe.test(src),
      'TerminalPanel.js empty-state branch was modified — LAYT-03 must leave the EmptyStateDashboard branch intact. Only wrap the ref=${containerRef} div in the non-empty branch.',
    ).toBe(true);
  });

  test('structural: mobile READ-ONLY banner is absent (mobile input enabled)', () => {
    const src = readTerminalPanelSrc();
    expect(
      /READ-ONLY: terminal input is disabled on mobile/.test(src),
      'TerminalPanel.js must NOT render the legacy mobile READ-ONLY banner; mobile input is enabled and only the server --read-only flag disables input now.',
    ).toBe(false);
  });

  test('structural: terminal.onData is not gated on !mobile', () => {
    const src = readTerminalPanelSrc();
    expect(
      /if \(!mobile\)\s*\{\s*inputDisposable\s*=\s*terminal\.onData/.test(src),
      'TerminalPanel.js must not gate terminal.onData behind !mobile; mobile input is enabled.',
    ).toBe(false);
    expect(
      /terminal\.onData\s*\(/.test(src),
      'TerminalPanel.js must retain an unconditional terminal.onData(...) call to forward keystrokes to the tmux bridge.',
    ).toBe(true);
  });

  test('structural: disableStdin is not OR-ed with mobile on status messages', () => {
    const src = readTerminalPanelSrc();
    expect(
      /disableStdin\s*=\s*!!payload\.readOnly\s*\|\|\s*mobile/.test(src),
      'TerminalPanel.js must not OR mobile into disableStdin; only payload.readOnly should disable input.',
    ).toBe(false);
  });

  // RUNTIME — skips without a fixture session; measures real computed styles.
  test('runtime: xterm wrapper has 16px padding on all four edges', async ({ page }) => {
    await page.setViewportSize({ width: 1280, height: 800 });
    await page.goto('/?t=test');
    await page.waitForSelector('header', { state: 'attached', timeout: 15000 });

    const sessionCount = await page.locator('button[data-session-id]').count();
    test.skip(sessionCount === 0, 'no fixture sessions — deferred to Phase 8 fixtures');

    await page.locator('button[data-session-id]').first().click();
    // Wait for .xterm to render after connection.
    const xtermAppeared = await page
      .waitForSelector('.xterm', { state: 'attached', timeout: 10000 })
      .then(() => true)
      .catch(() => false);
    test.skip(!xtermAppeared, 'xterm did not mount — terminal connect may have failed in test profile');

    const padding = await page.evaluate(() => {
      const xterm = document.querySelector('.xterm') as HTMLElement | null;
      if (!xterm) return null;
      // Walk up ancestors looking for the p-sp-16 wrapper.
      let node: HTMLElement | null = xterm.parentElement;
      while (node) {
        if (node.className && node.className.includes('p-sp-16')) {
          const cs = window.getComputedStyle(node);
          return {
            left: cs.paddingLeft,
            right: cs.paddingRight,
            top: cs.paddingTop,
            bottom: cs.paddingBottom,
          };
        }
        node = node.parentElement;
      }
      return null;
    });

    expect(padding, 'could not find a .p-sp-16 ancestor of .xterm — the wrapper fix was not applied').not.toBe(null);
    expect(padding!.left, 'padding-left must be 16px').toBe('16px');
    expect(padding!.right, 'padding-right must be 16px').toBe('16px');
    expect(padding!.top, 'padding-top must be 16px').toBe('16px');
    expect(padding!.bottom, 'padding-bottom must be 16px').toBe('16px');
  });

  test('runtime: xterm bounding rect is inset >= 15px from its wrapper on all sides', async ({ page }) => {
    await page.setViewportSize({ width: 1280, height: 800 });
    await page.goto('/?t=test');
    await page.waitForSelector('header', { state: 'attached', timeout: 15000 });

    const sessionCount = await page.locator('button[data-session-id]').count();
    test.skip(sessionCount === 0, 'no fixture sessions — deferred to Phase 8 fixtures');

    await page.locator('button[data-session-id]').first().click();
    const xtermAppeared = await page
      .waitForSelector('.xterm', { state: 'attached', timeout: 10000 })
      .then(() => true)
      .catch(() => false);
    test.skip(!xtermAppeared, 'xterm did not mount — terminal connect may have failed');

    const inset = await page.evaluate(() => {
      const xterm = document.querySelector('.xterm') as HTMLElement | null;
      if (!xterm) return null;
      let wrapper: HTMLElement | null = xterm.parentElement;
      while (wrapper) {
        if (wrapper.className && wrapper.className.includes('p-sp-16')) break;
        wrapper = wrapper.parentElement;
      }
      if (!wrapper) return null;
      const xr = xterm.getBoundingClientRect();
      const wr = wrapper.getBoundingClientRect();
      return {
        left: xr.left - wr.left,
        right: wr.right - xr.right,
        top: xr.top - wr.top,
        bottom: wr.bottom - xr.bottom,
      };
    });

    expect(inset, 'p-sp-16 ancestor wrapper not found').not.toBe(null);
    expect(inset!.left, `left inset ${inset!.left} must be >= 15`).toBeGreaterThanOrEqual(15);
    expect(inset!.right, `right inset ${inset!.right} must be >= 15`).toBeGreaterThanOrEqual(15);
    expect(inset!.top, `top inset ${inset!.top} must be >= 15`).toBeGreaterThanOrEqual(15);
    expect(inset!.bottom, `bottom inset ${inset!.bottom} must be >= 15`).toBeGreaterThanOrEqual(15);
  });
});
