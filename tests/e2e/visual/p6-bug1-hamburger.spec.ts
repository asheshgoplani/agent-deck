import { test, expect } from '@playwright/test';
import { readFileSync } from 'fs';
import { join } from 'path';

/**
 * Phase 6 / Plan 02 / Task 1: WEB-P0-1 regression spec.
 *
 * Root cause (LOCKED per 06-CONTEXT.md lines 37-56): the hamburger button in
 * Topbar.js has no explicit z-index and shares the `relative z-50` stacking
 * context with sibling right-side controls (Costs, ConnectionIndicator,
 * ThemeToggle, ProfileDropdown, Info, PushControls). On small viewports
 * (<=768px), flex overflow can push those siblings over the hamburger hit
 * area, intercepting pointer events. Users report the hamburger is
 * unclickable on mobile.
 *
 * Fix (LOCKED per 06-CONTEXT.md lines 155-167): systematic 7-level z-index
 * scale via Tailwind v4 @theme tokens in styles.src.css, with the hamburger
 * at --z-topbar-primary: 45 and the right-side controls wrapper at
 * --z-topbar: 40.
 *
 * TDD ORDER: this spec is committed in FAILING state in Task 1, then the
 * fix (z-scale + class migration + `make css`) lands in Task 2, flipping
 * all nine tests to green.
 *
 * STRUCTURAL FALLBACK: tests 1-6 readFileSync the source files and assert
 * literal markers of the fix. These run without a browser and provide the
 * failing-before-fix guarantee even if the DOM tests (7-9) are flaky or
 * skipped.
 */

const APP_DIR = join(__dirname, '..', '..', '..', 'internal', 'web', 'static', 'app');
const STATIC_DIR = join(__dirname, '..', '..', '..', 'internal', 'web', 'static');

// The seven z-index tokens that must exist in the @theme block of
// styles.src.css. These are taken verbatim from 06-CONTEXT.md lines 155-167.
const Z_TOKENS = [
  '--z-base:',
  '--z-sticky:',
  '--z-dropdown:',
  '--z-topbar:',
  '--z-topbar-primary:',
  '--z-modal:',
  '--z-toast:',
];

test.describe('WEB-P0-1 — hamburger z-index + tap-through', () => {
  test('structural: styles.src.css @theme block contains all 7 z-index tokens', () => {
    const src = readFileSync(join(STATIC_DIR, 'styles.src.css'), 'utf-8');
    for (const token of Z_TOKENS) {
      expect(
        src.includes(token),
        `styles.src.css must define ${token} in the @theme block per 06-CONTEXT.md lines 155-167.`,
      ).toBe(true);
    }
  });

  test('structural: compiled styles.css contains .z-topbar-primary utility class', () => {
    const css = readFileSync(join(STATIC_DIR, 'styles.css'), 'utf-8');
    // Tailwind v4 emits the class as `.z-topbar-primary { ... }` (unminified)
    // or `.z-topbar-primary{...}` (minified). Either form counts.
    expect(
      /\.z-topbar-primary\s*\{/.test(css),
      'styles.css (regenerated via `make css`) must contain a .z-topbar-primary class. Did you run `make css` after editing styles.src.css and adding the class to a .js file?',
    ).toBe(true);
  });

  test('structural: Topbar.js hamburger has z-topbar-primary + pointer-events-auto + relative', () => {
    const src = readFileSync(join(APP_DIR, 'Topbar.js'), 'utf-8');
    // Find the hamburger button — identified by its aria-label containing
    // "sidebar" (the label is `${sidebarOpen ? 'Close sidebar' : 'Open sidebar'}`).
    const hamburgerStart = src.indexOf("aria-label=${sidebarOpen ? 'Close sidebar'");
    expect(hamburgerStart, 'Topbar.js must contain the hamburger aria-label').toBeGreaterThan(-1);
    // Walk backwards to find the class="..." attribute for the same button.
    // 600 chars is enough to cover the class attribute without bleeding into
    // the previous <button> tag.
    const classBefore = src.slice(Math.max(0, hamburgerStart - 600), hamburgerStart);
    expect(
      /z-topbar-primary/.test(classBefore),
      'hamburger button in Topbar.js must have `z-topbar-primary` class (06-CONTEXT.md line 50).',
    ).toBe(true);
    expect(
      /pointer-events-auto/.test(classBefore),
      'hamburger button in Topbar.js must have `pointer-events-auto` class (06-CONTEXT.md line 50).',
    ).toBe(true);
    expect(
      /\brelative\b/.test(classBefore),
      'hamburger button in Topbar.js must have `relative` class to establish a local stacking context (z-index only applies to positioned elements).',
    ).toBe(true);
  });

  test('structural: Topbar.js right-side controls use z-topbar (not z-topbar-primary)', () => {
    const src = readFileSync(join(APP_DIR, 'Topbar.js'), 'utf-8');
    // The right-side container holds Costs / ConnectionIndicator / ThemeToggle
    // / ProfileDropdown / Info / PushControls. Find the `<${ConnectionIndicator}`
    // component reference (NOT the import at the top of the file) — the
    // enclosing div's class must contain `z-topbar` and NOT `z-topbar-primary`.
    const connIdx = src.indexOf('<${ConnectionIndicator}');
    expect(
      connIdx,
      'Topbar.js must render <${ConnectionIndicator} in the right-side controls wrapper',
    ).toBeGreaterThan(-1);
    // Walk backwards up to 1500 chars to find the enclosing div's class attribute.
    const classBefore = src.slice(Math.max(0, connIdx - 1500), connIdx);
    // Match `z-topbar` where the next char is NOT `-` (so `z-topbar-primary`
    // does not count). Negative lookahead makes this explicit.
    expect(
      /\bz-topbar(?!-)/.test(classBefore),
      'right-side controls wrapper in Topbar.js must contain `z-topbar` class (not `z-topbar-primary`). 06-CONTEXT.md line 52.',
    ).toBe(true);
  });

  test('structural: Toast.js ToastContainer uses z-toast (not z-[100])', () => {
    const src = readFileSync(join(APP_DIR, 'Toast.js'), 'utf-8');
    expect(
      /z-toast\b/.test(src),
      'Toast.js must use `z-toast` utility class (06-CONTEXT.md line 53).',
    ).toBe(true);
    expect(
      /z-\[100\]/.test(src),
      'Toast.js must NOT use the arbitrary `z-[100]` utility — migrate to `z-toast`.',
    ).toBe(false);
  });

  test('structural: ProfileDropdown.js open listbox uses z-dropdown', () => {
    const src = readFileSync(join(APP_DIR, 'ProfileDropdown.js'), 'utf-8');
    // After 06-01, the multi-profile branch has an absolute-positioned
    // listbox. This plan migrates it to z-dropdown.
    expect(
      /z-dropdown/.test(src),
      'ProfileDropdown.js listbox must use `z-dropdown` (multi-profile branch).',
    ).toBe(true);
  });

  test('DOM 375x667: elementFromPoint at hamburger center resolves to the hamburger', async ({ page }) => {
    await page.setViewportSize({ width: 375, height: 667 });
    await page.goto('/?t=test');
    await page.waitForSelector('header', { state: 'attached', timeout: 15000 });
    const hamburger = page.locator('header button[aria-label*="sidebar"]').first();
    await expect(hamburger).toBeVisible();
    const box = await hamburger.boundingBox();
    expect(box).not.toBeNull();
    if (!box) return;
    const cx = box.x + box.width / 2;
    const cy = box.y + box.height / 2;
    const hit = await page.evaluate(([x, y]) => {
      const el = document.elementFromPoint(x as number, y as number);
      if (!el) return '(null)';
      // Walk up looking for the hamburger aria-label. If the pointer lands
      // on an SVG child of the button, the walk will find the button.
      let cur: Element | null = el;
      while (cur) {
        const al = cur.getAttribute && cur.getAttribute('aria-label');
        if (al && /sidebar/i.test(al)) return 'hamburger';
        cur = cur.parentElement;
      }
      return (el.tagName || '?') + '#' + ((el as HTMLElement).id || '') + '.' + ((el.className || '') + '').toString().slice(0, 80);
    }, [cx, cy]);
    expect(
      hit,
      `elementFromPoint at hamburger center must resolve to the hamburger button, got: ${hit}`,
    ).toBe('hamburger');
  });

  test('DOM 375x667: clicking hamburger toggles aria-expanded', async ({ page }) => {
    await page.setViewportSize({ width: 375, height: 667 });
    // Seed localStorage so sidebar starts closed.
    await page.addInitScript(() => {
      try { localStorage.setItem('agentdeck.sidebarOpen', 'false'); } catch (_) { /* ignore */ }
    });
    await page.goto('/?t=test');
    await page.waitForSelector('header', { state: 'attached', timeout: 15000 });
    const hamburger = page.locator('header button[aria-label*="sidebar"]').first();
    const expandedBefore = await hamburger.getAttribute('aria-expanded');
    await hamburger.click();
    await page.waitForTimeout(200);
    const expandedAfter = await hamburger.getAttribute('aria-expanded');
    expect(expandedAfter, 'aria-expanded must change after clicking the hamburger').not.toBe(expandedBefore);
  });

  test('DOM 375x667: hamburger bounding box is >=44x44 (WCAG 2.5.5)', async ({ page }) => {
    await page.setViewportSize({ width: 375, height: 667 });
    await page.goto('/?t=test');
    await page.waitForSelector('header', { state: 'attached', timeout: 15000 });
    const hamburger = page.locator('header button[aria-label*="sidebar"]').first();
    const box = await hamburger.boundingBox();
    expect(box).not.toBeNull();
    if (box) {
      expect(box.width, `hamburger width must be >=44px (WCAG 2.5.5), got ${box.width}`).toBeGreaterThanOrEqual(44);
      expect(box.height, `hamburger height must be >=44px (WCAG 2.5.5), got ${box.height}`).toBeGreaterThanOrEqual(44);
    }
  });
});
