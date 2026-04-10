import { test, expect } from '@playwright/test';
import { readFileSync } from 'fs';
import { join } from 'path';

/**
 * Phase 7 / Plan 02 / Task 1: WEB-P1-2 regression test
 *
 * Asserts that the sidebar uses fluid width via `clamp(260px, 22vw, 380px)`,
 * the SidebarResizeHandle drag component is removed (deferred to v1.6),
 * and the aside element no longer carries an inline `style="width: Npx"`.
 *
 * Layer 1 (structural, always runs):
 *   - styles.src.css declares the `.sidebar-fluid` utility with the locked formula
 *   - AppShell.js drops `function SidebarResizeHandle`
 *   - AppShell.js <aside> uses class `sidebar-fluid` (Tailwind v4 source-scans this)
 *   - AppShell.js <aside> no longer carries the inline width style attribute
 *
 * Layer 2 (DOM at three viewports):
 *   - 1920x1080: aside width clamped to 380 px ceiling
 *   - 1280x800: aside width ~282 px (22vw of 1280 = 281.6)
 *   - 375x667 mobile: aside width ~288 px (w-72 unaffected by the clamp)
 *
 * Root cause (LOCKED per 07-02-PLAN.md): the existing fixed 280 px width plus
 * a manual drag handle ignores the 1920 px monitor case. clamp() ships the
 * quick win without re-architecting the resize state.
 *
 * TDD ORDER: failing in Task 1, green in Task 2.
 */

const STYLES_SRC_PATH = join(
  __dirname, '..', '..', '..',
  'internal', 'web', 'static', 'styles.src.css',
);
const APP_SHELL_PATH = join(
  __dirname, '..', '..', '..',
  'internal', 'web', 'static', 'app', 'AppShell.js',
);

test.describe('WEB-P1-2 -- sidebar uses fluid width clamp(260px, 22vw, 380px)', () => {
  // ===== Layer 1: structural =====

  test('structural: styles.src.css declares the .sidebar-fluid utility with the locked clamp formula', () => {
    const src = readFileSync(STYLES_SRC_PATH, 'utf-8');
    expect(
      src.includes('clamp(260px, 22vw, 380px)'),
      'styles.src.css must contain the literal `clamp(260px, 22vw, 380px)` per WEB-P1-2 (07-02-PLAN.md). The .sidebar-fluid utility uses this formula.',
    ).toBe(true);
  });

  test('structural: AppShell.js no longer defines function SidebarResizeHandle', () => {
    const src = readFileSync(APP_SHELL_PATH, 'utf-8');
    expect(
      /function\s+SidebarResizeHandle/.test(src),
      'AppShell.js must NOT define SidebarResizeHandle -- drag-to-resize is deferred to v1.6 (V16-WEB-01). Delete the function definition AND its <${SidebarResizeHandle} /> usage.',
    ).toBe(false);
  });

  test('structural: AppShell.js <aside> class string contains sidebar-fluid', () => {
    const src = readFileSync(APP_SHELL_PATH, 'utf-8');
    const asideMatch = src.match(/<aside[^>]*class="([^"]+)"/);
    expect(asideMatch, 'AppShell.js must contain a <aside class="..."> element').not.toBeNull();
    if (asideMatch) {
      expect(
        /sidebar-fluid/.test(asideMatch[1]),
        `<aside> class string must contain "sidebar-fluid". Got: ${asideMatch[1]}`,
      ).toBe(true);
    }
  });

  test('structural: AppShell.js <aside> drops the inline width style attribute', () => {
    const src = readFileSync(APP_SHELL_PATH, 'utf-8');
    // Look for the specific old pattern: style=${isDesktop() ? `width: ${sidebarWidth}px;` : ''}
    expect(
      /sidebarWidth.*px/.test(src),
      'AppShell.js must NOT contain `${sidebarWidth}px` inline style -- Tailwind clamp() handles desktop width via the .sidebar-fluid utility class.',
    ).toBe(false);
  });

  // ===== Layer 2: DOM =====

  test('DOM 1920x1080: aside width is clamped at the 380 px ceiling', async ({ page }) => {
    await page.setViewportSize({ width: 1920, height: 1080 });
    await page.goto('/?t=test');
    await page.waitForSelector('aside', { state: 'attached', timeout: 15000 });
    await page.waitForTimeout(200);
    const box = await page.locator('aside').first().boundingBox();
    expect(box, 'aside must have a bounding box').not.toBeNull();
    if (box) {
      // 1920 * 0.22 = 422.4 -> clamped to 380
      expect(
        box.width,
        `at 1920x1080, sidebar width must be clamped to 380 +/- 5 px; got ${box.width}`,
      ).toBeGreaterThanOrEqual(370);
      expect(box.width).toBeLessThanOrEqual(390);
    }
  });

  test('DOM 1280x800: aside width is ~282 px (22vw of 1280)', async ({ page }) => {
    await page.setViewportSize({ width: 1280, height: 800 });
    await page.goto('/?t=test');
    await page.waitForSelector('aside', { state: 'attached', timeout: 15000 });
    await page.waitForTimeout(200);
    const box = await page.locator('aside').first().boundingBox();
    expect(box, 'aside must have a bounding box').not.toBeNull();
    if (box) {
      // 1280 * 0.22 = 281.6
      expect(
        box.width,
        `at 1280x800, sidebar width must be ~282 px (22vw); got ${box.width}`,
      ).toBeGreaterThanOrEqual(270);
      expect(box.width).toBeLessThanOrEqual(295);
    }
  });

  test('DOM 375x667 mobile: aside is unaffected by clamp (w-72 = 288 px)', async ({ page }) => {
    await page.setViewportSize({ width: 375, height: 667 });
    await page.goto('/?t=test');
    await page.waitForSelector('header', { state: 'attached', timeout: 15000 });
    // Open the sidebar drawer if it isn't already
    const aside = page.locator('aside').first();
    const visible = await aside.isVisible().catch(() => false);
    if (!visible) {
      await page.locator('header button[aria-label="Open sidebar"]').click().catch(() => {});
      await page.waitForTimeout(300);
    }
    const box = await aside.boundingBox();
    expect(box, 'mobile aside must have a bounding box once visible').not.toBeNull();
    if (box) {
      // w-72 = 18rem = 288 px
      expect(
        box.width,
        `mobile sidebar must be ~288 px (w-72), unaffected by the desktop clamp; got ${box.width}`,
      ).toBeGreaterThanOrEqual(280);
      expect(box.width).toBeLessThanOrEqual(310);
    }
  });
});
