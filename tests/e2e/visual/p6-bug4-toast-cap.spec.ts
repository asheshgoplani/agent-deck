import { test, expect } from '@playwright/test';
import { readFileSync, existsSync } from 'fs';
import { join } from 'path';

/**
 * Phase 6 / Plan 04 / Task 1: WEB-P0-4 + POL-7 regression test
 *
 * Locked contract (06-CONTEXT.md lines 84-96):
 *   1. Toast stack capped at 3 visible at once.
 *   2. When a 4th toast arrives, evict oldest non-error first (FIFO).
 *      Errors are evicted only if all 3 visible are errors AND a new error
 *      arrives.
 *   3. info / success toasts auto-dismiss after 5 seconds.
 *   4. error toasts do NOT auto-dismiss (require explicit X click).
 *   5. Dismissed toasts push into toastHistorySignal capped at 50 entries
 *      and persisted to localStorage key `agentdeck_toast_history`.
 *   6. ToastHistoryDrawer component (toggleable from a Topbar button)
 *      shows historical toasts with errors highlighted.
 *   7. Errors use aria-live="assertive"; info/success use aria-live="polite".
 *
 * TDD ORDER: this spec is committed in FAILING state in Task 1, then the
 * implementation lands across Tasks 2-4, flipping each test to green.
 */

const APP_DIR = join(__dirname, '..', '..', '..', 'internal', 'web', 'static', 'app');

function readApp(name: string): string {
  return readFileSync(join(APP_DIR, name), 'utf-8');
}

test.describe('WEB-P0-4 + POL-7 — toast cap, auto-dismiss, history drawer', () => {
  test('structural: state.js exports toastHistorySignal', () => {
    const src = readApp('state.js');
    expect(/export const toastHistorySignal/.test(src),
      'state.js must export toastHistorySignal (06-CONTEXT.md line 93).').toBe(true);
  });

  test('structural: state.js exports toastHistoryOpenSignal', () => {
    const src = readApp('state.js');
    expect(/export const toastHistoryOpenSignal/.test(src),
      'state.js must export toastHistoryOpenSignal for the drawer open/close state.').toBe(true);
  });

  test('structural: Toast.js imports toastHistorySignal', () => {
    const src = readApp('Toast.js');
    expect(/toastHistorySignal/.test(src),
      'Toast.js must import and use toastHistorySignal.').toBe(true);
  });

  test('structural: Toast.js references the localStorage key agentdeck_toast_history', () => {
    const src = readApp('Toast.js');
    expect(src.includes('agentdeck_toast_history'),
      'Toast.js must persist history to localStorage key `agentdeck_toast_history` (06-CONTEXT.md line 93).').toBe(true);
  });

  test('structural: Toast.js does NOT auto-dismiss errors', () => {
    const src = readApp('Toast.js');
    expect(
      /type\s*!==\s*['"]error['"]/.test(src),
      "Toast.js setTimeout must be guarded by `type !== 'error'` so errors do not auto-dismiss (06-CONTEXT.md line 92).",
    ).toBe(true);
  });

  test('structural: Toast.js caps visible stack at 3 (literal 3)', () => {
    const src = readApp('Toast.js');
    expect(/length\s*[>]=?\s*3/.test(src),
      'Toast.js must check for length >= 3 or > 3 (literal 3) to cap the visible stack (06-CONTEXT.md line 91).').toBe(true);
  });

  test('structural: Toast.js uses aria-live assertive for errors', () => {
    const src = readApp('Toast.js');
    expect(/assertive/.test(src),
      'Toast.js must use aria-live="assertive" for error toasts (06-CONTEXT.md line 95).').toBe(true);
  });

  test('structural: ToastHistoryDrawer.js exists and exports a component', () => {
    const path = join(APP_DIR, 'ToastHistoryDrawer.js');
    expect(existsSync(path), 'ToastHistoryDrawer.js must exist per 06-CONTEXT.md line 96.').toBe(true);
    const src = readFileSync(path, 'utf-8');
    expect(/export function ToastHistoryDrawer/.test(src),
      'ToastHistoryDrawer.js must export a ToastHistoryDrawer component.').toBe(true);
    expect(/export function ToastHistoryDrawerToggle/.test(src),
      'ToastHistoryDrawer.js must export a ToastHistoryDrawerToggle component.').toBe(true);
  });

  test('structural: Topbar.js imports ToastHistoryDrawerToggle', () => {
    const src = readApp('Topbar.js');
    expect(/ToastHistoryDrawerToggle/.test(src),
      'Topbar.js must import and render ToastHistoryDrawerToggle per 06-CONTEXT.md line 96.').toBe(true);
  });

  test('structural: AppShell.js mounts ToastHistoryDrawer', () => {
    const src = readApp('AppShell.js');
    expect(/ToastHistoryDrawer/.test(src),
      'AppShell.js must import and render ToastHistoryDrawer alongside ToastContainer.').toBe(true);
  });

  test('DOM: 5 info toasts then exactly 3 visible, 2 in history', async ({ page }) => {
    await page.goto('/?t=test');
    await page.waitForSelector('header', { state: 'attached', timeout: 15000 });
    const result = await page.evaluate(async () => {
      const mod: any = await import('/static/app/Toast.js');
      const state: any = await import('/static/app/state.js');
      state.toastsSignal.value = [];
      state.toastHistorySignal.value = [];
      mod.addToast('msg1', 'info');
      mod.addToast('msg2', 'info');
      mod.addToast('msg3', 'info');
      mod.addToast('msg4', 'info');
      mod.addToast('msg5', 'info');
      return {
        visible: state.toastsSignal.value.length,
        history: state.toastHistorySignal.value.length,
        visibleMessages: state.toastsSignal.value.map((t: any) => t.message),
      };
    });
    expect(result.visible, 'visible cap must be 3').toBe(3);
    expect(result.history, 'history must contain the 2 evicted toasts').toBeGreaterThanOrEqual(2);
    expect(result.visibleMessages).toEqual(['msg3', 'msg4', 'msg5']);
  });

  test('DOM: error toast does NOT auto-dismiss after 5.5 seconds', async ({ page }) => {
    await page.goto('/?t=test');
    await page.waitForSelector('header', { state: 'attached', timeout: 15000 });
    await page.evaluate(async () => {
      const mod: any = await import('/static/app/Toast.js');
      const state: any = await import('/static/app/state.js');
      state.toastsSignal.value = [];
      mod.addToast('critical error', 'error');
    });
    await page.waitForTimeout(5500);
    const stillPresent = await page.evaluate(async () => {
      const state: any = await import('/static/app/state.js');
      return state.toastsSignal.value.length;
    });
    expect(stillPresent, 'error toast must remain after 5.5s (requires explicit dismiss)').toBe(1);
  });

  test('DOM: info toast auto-dismisses after 5 seconds', async ({ page }) => {
    await page.goto('/?t=test');
    await page.waitForSelector('header', { state: 'attached', timeout: 15000 });
    await page.evaluate(async () => {
      const mod: any = await import('/static/app/Toast.js');
      const state: any = await import('/static/app/state.js');
      state.toastsSignal.value = [];
      mod.addToast('transient info', 'info');
    });
    await page.waitForTimeout(5500);
    const remaining = await page.evaluate(async () => {
      const state: any = await import('/static/app/state.js');
      return state.toastsSignal.value.length;
    });
    expect(remaining, 'info toast must auto-dismiss after 5s').toBe(0);
  });
});
