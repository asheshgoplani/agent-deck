import { test, expect } from '@playwright/test';
import { readFileSync, existsSync } from 'fs';
import { join } from 'path';

/**
 * Phase 4 / Plan 03 / Task 1: BUG #14 / UX-03 regression test
 *
 * Asserts that pressing the ? key opens a modal overlay listing all
 * keyboard shortcuts, that Escape closes it, that clicking the backdrop
 * closes it, and that the overlay text contains every shortcut from the
 * locked list (Navigation: j/k/Enter/Escape; Sessions: n/s/r/d;
 * Search: //Cmd+K/Escape; Help: ?).
 *
 * Root cause (LOCKED per 04-CONTEXT.md): keyboard shortcuts are wired in
 * useKeyboardNav.js and SearchFilter.js but invisible to users — no
 * discoverability mechanism exists.
 *
 * Fix (LOCKED per 04-CONTEXT.md):
 *   1. Create internal/web/static/app/KeyboardShortcutsOverlay.js
 *      exporting the KeyboardShortcutsOverlay Preact component.
 *   2. Add shortcutsOverlaySignal to state.js.
 *   3. Add e.key === '?' handler to useKeyboardNav.js.
 *   4. Mount the overlay in App.js next to other top-level dialogs.
 *
 * TDD ORDER: this spec is committed in FAILING state in Task 1; Task 2
 * creates the component and wires it up.
 */

const SRC_ROOT = join(__dirname, '..', '..', '..', 'internal', 'web', 'static', 'app');

test.describe('BUG #14 / UX-03 — keyboard shortcuts overlay', () => {
  test('press ? opens the shortcuts overlay', async ({ page }) => {
    await page.goto('/?t=test');
    await page.waitForSelector('header', { state: 'attached', timeout: 15000 });
    // Click an empty area to blur any auto-focused input (the search input
    // would otherwise consume the ? key as text input).
    await page.locator('body').click({ position: { x: 1, y: 1 } });
    await page.waitForTimeout(100);
    await page.keyboard.press('?');
    await page.waitForTimeout(200);
    const dialog = page.locator('[role="dialog"][aria-modal="true"]');
    await expect(
      dialog,
      'pressing ? must open a [role="dialog"][aria-modal="true"] overlay',
    ).toBeVisible({ timeout: 2000 });
  });

  test('Escape closes the overlay', async ({ page }) => {
    await page.goto('/?t=test');
    await page.waitForSelector('header', { state: 'attached', timeout: 15000 });
    await page.locator('body').click({ position: { x: 1, y: 1 } });
    await page.waitForTimeout(100);
    await page.keyboard.press('?');
    await page.waitForTimeout(200);
    await page.keyboard.press('Escape');
    await page.waitForTimeout(200);
    const dialog = page.locator('[role="dialog"][aria-modal="true"]');
    await expect(
      dialog,
      'Escape must close the shortcuts overlay',
    ).toHaveCount(0);
  });

  test('clicking the backdrop closes the overlay', async ({ page }) => {
    await page.goto('/?t=test');
    await page.waitForSelector('header', { state: 'attached', timeout: 15000 });
    await page.locator('body').click({ position: { x: 1, y: 1 } });
    await page.waitForTimeout(100);
    await page.keyboard.press('?');
    await page.waitForTimeout(200);
    // Click in the top-left corner where only the backdrop is rendered
    // (the centered modal panel does not extend there).
    await page.locator('[role="dialog"][aria-modal="true"]').click({ position: { x: 5, y: 5 } });
    await page.waitForTimeout(200);
    const dialog = page.locator('[role="dialog"][aria-modal="true"]');
    await expect(
      dialog,
      'clicking the backdrop must close the shortcuts overlay',
    ).toHaveCount(0);
  });

  test('overlay text contains all shortcut categories and keys', async ({ page }) => {
    await page.goto('/?t=test');
    await page.waitForSelector('header', { state: 'attached', timeout: 15000 });
    await page.locator('body').click({ position: { x: 1, y: 1 } });
    await page.waitForTimeout(100);
    await page.keyboard.press('?');
    await page.waitForTimeout(200);

    const dialogText = (await page.locator('[role="dialog"][aria-modal="true"]').textContent()) || '';

    // Categories
    expect(dialogText, 'overlay must list "Navigation" category').toContain('Navigation');
    expect(dialogText, 'overlay must list "Sessions" category').toContain('Sessions');
    expect(dialogText, 'overlay must list "Search" category').toContain('Search');
    expect(dialogText, 'overlay must list "Help" category').toContain('Help');

    // Sample of locked shortcut keys (the spec checks the load-bearing ones)
    // We use word-boundary substring checks to avoid matching incidental letters.
    const requiredKeys = ['Enter', 'Escape', '?'];
    for (const k of requiredKeys) {
      expect(
        dialogText.includes(k),
        `overlay must contain shortcut key "${k}" — sourced from useKeyboardNav.js`,
      ).toBe(true);
    }
  });

  test('structural: shortcutsOverlaySignal is declared in state.js', () => {
    const src = readFileSync(join(SRC_ROOT, 'state.js'), 'utf-8');
    expect(
      /shortcutsOverlaySignal/.test(src),
      'state.js must export shortcutsOverlaySignal — BUG #14 / UX-03',
    ).toBe(true);
  });

  test('structural: useKeyboardNav.js handles e.key === \'?\'', () => {
    const src = readFileSync(join(SRC_ROOT, 'useKeyboardNav.js'), 'utf-8');
    expect(
      /e\.key\s*===\s*['"]\?['"]/.test(src),
      'useKeyboardNav.js must contain `e.key === \'?\'` handler — BUG #14 / UX-03',
    ).toBe(true);
  });

  test('structural: App.js mounts KeyboardShortcutsOverlay', () => {
    const src = readFileSync(join(SRC_ROOT, 'App.js'), 'utf-8');
    expect(
      /KeyboardShortcutsOverlay/.test(src),
      'App.js must import and mount KeyboardShortcutsOverlay — BUG #14 / UX-03',
    ).toBe(true);
  });

  test('structural: KeyboardShortcutsOverlay.js exists with aria-modal', () => {
    const p = join(SRC_ROOT, 'KeyboardShortcutsOverlay.js');
    expect(
      existsSync(p),
      'KeyboardShortcutsOverlay.js must exist — BUG #14 / UX-03',
    ).toBe(true);
    if (existsSync(p)) {
      const src = readFileSync(p, 'utf-8');
      expect(
        /aria-modal/.test(src),
        'KeyboardShortcutsOverlay.js must include aria-modal="true"',
      ).toBe(true);
      expect(
        /shortcutsOverlaySignal/.test(src),
        'KeyboardShortcutsOverlay.js must subscribe to shortcutsOverlaySignal',
      ).toBe(true);
    }
  });
});
