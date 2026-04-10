import { test, expect } from '@playwright/test';
import { readFileSync } from 'fs';
import { join } from 'path';

/**
 * Phase 7 / Plan 04 / Task 1: WEB-P1-5 regression test
 *
 * Asserts that on viewports <600 px, the topbar collapses Costs, Connection,
 * Theme, Profile, Info, and Push controls into an overflow `⋯` menu.
 *
 * Layer 1 (structural):
 *   - Topbar.js uses Tailwind `max-[599px]:` arbitrary breakpoint
 *   - Topbar.js renders an overflow trigger button with aria-haspopup="menu"
 *   - Topbar.js trigger uses z-topbar-primary (the Phase 6 06-02 systematic scale)
 *   - Topbar.js wires Escape-key dismissal
 *   - styles.src.css declares --z-topbar-primary (proves 06-02 shipped)
 *
 * Layer 2 (DOM at iPhone SE 375x667):
 *   - Header contains <=4 visible buttons (hamburger + brand + overflow + maybe one primary)
 *   - Tapping the overflow trigger reveals the collapsed items
 *   - Hamburger remains 44x44 px
 *
 * Layer 3 (DOM at desktop 1280x800):
 *   - Overflow trigger is hidden
 *   - All original inline topbar buttons remain visible (no regression)
 *
 * Cross-phase dependency: Phase 6 plan 06-02 (WEB-P0-1) must have shipped first.
 * Test 7 below catches that case explicitly.
 *
 * TDD ORDER: failing in Task 1, green in Task 2.
 */

const TOPBAR_PATH = join(
  __dirname, '..', '..', '..',
  'internal', 'web', 'static', 'app', 'Topbar.js',
);
const STYLES_SRC_PATH = join(
  __dirname, '..', '..', '..',
  'internal', 'web', 'static', 'styles.src.css',
);

test.describe('WEB-P1-5 — mobile topbar collapses to overflow menu <600 px', () => {
  // ===== Layer 1: structural =====

  test('structural: Topbar.js uses Tailwind max-[599px] arbitrary breakpoint', () => {
    const src = readFileSync(TOPBAR_PATH, 'utf-8');
    expect(
      /max-\[599px\]/.test(src),
      'Topbar.js must use Tailwind `max-[599px]:` arbitrary mobile breakpoint per WEB-P1-5.',
    ).toBe(true);
  });

  test('structural: Topbar.js renders overflow trigger with aria-haspopup="menu"', () => {
    const src = readFileSync(TOPBAR_PATH, 'utf-8');
    expect(
      /aria-haspopup="menu"/.test(src),
      'Topbar.js must render an overflow trigger button with `aria-haspopup="menu"`.',
    ).toBe(true);
  });

  test('structural: Topbar.js overflow trigger has aria-label', () => {
    const src = readFileSync(TOPBAR_PATH, 'utf-8');
    // Accept either "More options" or "Overflow menu" labelling
    expect(
      /aria-label="(More options|Overflow menu|More)"/.test(src),
      'Topbar.js overflow trigger must have aria-label="More options" or "Overflow menu" for screen readers.',
    ).toBe(true);
  });

  test('structural: Topbar.js overflow trigger uses z-topbar-primary scale', () => {
    const src = readFileSync(TOPBAR_PATH, 'utf-8');
    expect(
      /z-topbar-primary/.test(src),
      'Topbar.js overflow popover must use the `z-topbar-primary` Tailwind utility from Phase 6 plan 06-02.',
    ).toBe(true);
  });

  test('structural: Topbar.js wires Escape-key dismissal', () => {
    const src = readFileSync(TOPBAR_PATH, 'utf-8');
    expect(
      /e\.key\s*===\s*['"]Escape['"]/.test(src),
      'Topbar.js must dismiss the overflow menu on Escape key per a11y requirements.',
    ).toBe(true);
  });

  test('structural: PREREQUISITE — styles.src.css declares --z-topbar-primary token', () => {
    const src = readFileSync(STYLES_SRC_PATH, 'utf-8');
    expect(
      /--z-topbar-primary/.test(src),
      'PREREQUISITE: Phase 6 plan 06-02 (WEB-P0-1 systematic z-index scale) must have shipped — styles.src.css must declare `--z-topbar-primary`. Run /gsd:execute-phase 6 to ship 06-02 first.',
    ).toBe(true);
  });

  // ===== Layer 2: mobile DOM (375x667) =====

  test('DOM 375x667: header contains <=4 visible buttons (hamburger + brand + overflow + <=1 primary)', async ({ page }) => {
    await page.setViewportSize({ width: 375, height: 667 });
    await page.goto('/?t=test');
    await page.waitForSelector('header', { state: 'attached', timeout: 15000 });
    await page.waitForTimeout(200);
    // Count visible buttons. `offsetParent === null` when the element OR any
    // ancestor is `display: none`, which is the correct check for "actually
    // rendered to the user" — the Topbar collapses the desktop controls via
    // `max-[599px]:hidden` on the PARENT wrapper, so the child buttons must
    // be detected as hidden through their ancestor's computed display.
    const visibleButtonCount = await page.locator('header button').evaluateAll(buttons =>
      buttons.filter(b => {
        const el = b as HTMLElement;
        if (el.offsetParent === null) return false;
        const style = window.getComputedStyle(el);
        return style.visibility !== 'hidden';
      }).length,
    );
    expect(
      visibleButtonCount,
      `at 375x667, header must have <=4 visible buttons (hamburger + maybe primary + overflow); got ${visibleButtonCount}`,
    ).toBeLessThanOrEqual(4);
  });

  test('DOM 375x667: tapping overflow trigger reveals collapsed items (Costs, Info)', async ({ page }) => {
    await page.setViewportSize({ width: 375, height: 667 });
    await page.goto('/?t=test');
    await page.waitForSelector('header', { state: 'attached', timeout: 15000 });
    const trigger = page.locator('header button[aria-haspopup="menu"]');
    await expect(trigger).toHaveCount(1);
    await trigger.click();
    await page.waitForTimeout(200);
    // The popover should now be visible somewhere in the document
    const popover = page.locator('[role="menu"]');
    await expect(popover).toBeVisible();
    const popoverText = await popover.innerText();
    expect(
      popoverText.includes('Costs'),
      `overflow menu must contain "Costs"; got: ${popoverText}`,
    ).toBe(true);
    expect(
      popoverText.includes('Info'),
      `overflow menu must contain "Info"; got: ${popoverText}`,
    ).toBe(true);
  });

  test('DOM 375x667: Escape closes the overflow menu', async ({ page }) => {
    await page.setViewportSize({ width: 375, height: 667 });
    await page.goto('/?t=test');
    await page.waitForSelector('header', { state: 'attached', timeout: 15000 });
    const trigger = page.locator('header button[aria-haspopup="menu"]');
    await trigger.click();
    await expect(page.locator('[role="menu"]')).toBeVisible();
    await page.keyboard.press('Escape');
    await page.waitForTimeout(200);
    await expect(page.locator('[role="menu"]')).toHaveCount(0);
  });

  test('DOM 375x667: hamburger remains 44x44 px touch target', async ({ page }) => {
    await page.setViewportSize({ width: 375, height: 667 });
    await page.goto('/?t=test');
    await page.waitForSelector('header', { state: 'attached', timeout: 15000 });
    const hamburger = page.locator('header button[aria-label*="sidebar"]').first();
    await expect(hamburger).toHaveCount(1);
    const box = await hamburger.boundingBox();
    expect(box, 'hamburger must have a bounding box').not.toBeNull();
    if (box) {
      expect(box.width, `hamburger width >=44; got ${box.width}`).toBeGreaterThanOrEqual(44);
      expect(box.height, `hamburger height >=44; got ${box.height}`).toBeGreaterThanOrEqual(44);
    }
  });

  // ===== Layer 3: desktop DOM (1280x800) — no regression =====

  test('DOM 1280x800: overflow trigger is hidden, all inline topbar buttons visible', async ({ page }) => {
    await page.setViewportSize({ width: 1280, height: 800 });
    await page.goto('/?t=test');
    await page.waitForSelector('header', { state: 'attached', timeout: 15000 });
    await page.waitForTimeout(200);
    // Overflow trigger should be display:none on lg
    const triggerCount = await page.locator('header button[aria-haspopup="menu"]').count();
    if (triggerCount > 0) {
      const triggerVisible = await page.locator('header button[aria-haspopup="menu"]').first().isVisible();
      expect(
        triggerVisible,
        'on desktop, the overflow trigger button must be hidden (display: none via min-[600px]:hidden)',
      ).toBe(false);
    }
    // Costs button should be visible inline
    const headerText = await page.locator('header').innerText();
    expect(
      headerText.includes('Costs') || headerText.includes('Terminal'),
      'on desktop, the Costs/Terminal toggle must be visible inline in the header',
    ).toBe(true);
    expect(
      headerText.includes('Info'),
      'on desktop, the Info button must be visible inline in the header',
    ).toBe(true);
  });
});
