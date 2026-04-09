import { test, expect } from '@playwright/test';
import { readFileSync } from 'fs';
import { join } from 'path';

/**
 * Phase 2 / Plan 02 / Task 1: BUG #3 / CRIT-03 regression test
 *
 * Asserts that every visible session title span and group name span has
 * getBoundingClientRect width greater than 0 in both desktop (1280x800) and
 * mobile (375x812) viewports. Also asserts that at least one truncated row
 * has computed text-overflow ellipsis, proving the truncate utility is
 * active.
 *
 * Root cause hypothesis (LOCKED per 02-CONTEXT.md): the outer button in
 * SessionRow.js line 82 and GroupRow.js line 43 is missing `min-w-0`. Even
 * with `w-full`, when the parent li participates in the ul flex flex-col,
 * the flex-item default `min-width: auto` prevents the button from
 * shrinking below intrinsic content width. The inner `flex-1 truncate
 * min-w-0` on the title span then cannot kick in because the flex
 * container itself is at overflow, pushing the title to 0 px.
 *
 * Fix (LOCKED per 02-CONTEXT.md): add `min-w-0` to the outer button class
 * in both SessionRow.js and GroupRow.js. If still failing, also add to the
 * ul in SessionList.js.
 *
 * TDD ORDER: this spec is committed in FAILING state in Task 1, then the
 * fix lands in Task 2, flipping the spec to green.
 *
 * STRUCTURAL FALLBACK: if no fixture sessions render, the bounding-rect
 * tests skip. In that case the DOM-structural tests (outer button has
 * min-w-0) ALWAYS run and provide the failing-before-fix guarantee.
 */

interface ZeroWidthHit {
  role: 'session' | 'group';
  identifier: string;
  width: number;
  text: string;
}

async function gotoAndWait(
  page: import('@playwright/test').Page,
  viewport: { width: number; height: number },
): Promise<void> {
  await page.setViewportSize(viewport);
  await page.goto('/?t=test');
  await page.waitForSelector('header', { state: 'attached', timeout: 15000 });
  // preact-session-list is the id on the ul in SessionList.js line 95.
  await page
    .waitForSelector('#preact-session-list', { state: 'attached', timeout: 15000 })
    .catch(() => {
      // Empty state renders a different element; structural tests still cover zero-fixture case.
    });
  await page.waitForTimeout(500);
}

async function findZeroWidthSessionTitles(
  page: import('@playwright/test').Page,
): Promise<ZeroWidthHit[]> {
  return page.evaluate(() => {
    const out: Array<{ role: 'session'; identifier: string; width: number; text: string }> = [];
    const buttons = document.querySelectorAll('button[data-session-id]');
    for (let i = 0; i < buttons.length; i++) {
      const btn = buttons[i] as HTMLElement;
      const id = btn.getAttribute('data-session-id') || '(unknown)';
      const titleSpan = btn.querySelector('span.truncate') as HTMLElement | null;
      if (!titleSpan) {
        out.push({ role: 'session', identifier: id, width: -1, text: '(no .truncate span found)' });
        continue;
      }
      const rect = titleSpan.getBoundingClientRect();
      if (rect.width <= 0) {
        out.push({
          role: 'session',
          identifier: id,
          width: rect.width,
          text: (titleSpan.textContent || '').slice(0, 80),
        });
      }
    }
    return out;
  });
}

async function findZeroWidthGroupTitles(
  page: import('@playwright/test').Page,
): Promise<ZeroWidthHit[]> {
  return page.evaluate(() => {
    const out: Array<{ role: 'group'; identifier: string; width: number; text: string }> = [];
    // Scope to group rows inside the Preact session list. Topbar hamburger,
    // info drawer, and ProfileDropdown also use [aria-expanded] — scoping to
    // #preact-session-list excludes those false positives so the gate
    // measures only GroupRow.js title spans.
    const buttons = document.querySelectorAll('#preact-session-list button[aria-expanded]');
    for (let i = 0; i < buttons.length; i++) {
      const btn = buttons[i] as HTMLElement;
      const label = (btn.textContent || '(unknown)').trim().slice(0, 40);
      const titleSpan = btn.querySelector('span.truncate') as HTMLElement | null;
      if (!titleSpan) {
        out.push({ role: 'group', identifier: label, width: -1, text: '(no .truncate span found)' });
        continue;
      }
      const rect = titleSpan.getBoundingClientRect();
      if (rect.width <= 0) {
        out.push({
          role: 'group',
          identifier: label,
          width: rect.width,
          text: (titleSpan.textContent || '').slice(0, 80),
        });
      }
    }
    return out;
  });
}

async function hasEllipsisRow(page: import('@playwright/test').Page): Promise<boolean> {
  return page.evaluate(() => {
    const nodes = document.querySelectorAll(
      'button[data-session-id] span.truncate, #preact-session-list button[aria-expanded] span.truncate',
    );
    for (let i = 0; i < nodes.length; i++) {
      const el = nodes[i] as Element;
      const cs = window.getComputedStyle(el);
      if (cs.textOverflow === 'ellipsis' && cs.overflow === 'hidden') return true;
    }
    return false;
  });
}

function formatHits(hits: ZeroWidthHit[]): string {
  return hits.map((h) => `  ${h.role}[${h.identifier}] width=${h.width} text=${h.text}`).join('\n');
}

test.describe('BUG #3 / CRIT-03 — session/group name bounding-box width greater than 0', () => {
  test('desktop 1280x800: every session title span has width > 0', async ({ page }) => {
    await gotoAndWait(page, { width: 1280, height: 800 });
    const count = await page.locator('button[data-session-id]').count();
    test.skip(count === 0, 'no fixture sessions available — rely on structural test + Phase 8 fixtures');
    const zero = await findZeroWidthSessionTitles(page);
    expect(
      zero,
      `desktop: ${zero.length} session title spans have 0-px width — BUG #3.\n${formatHits(zero)}`,
    ).toEqual([]);
  });

  test('desktop 1280x800: every group title span has width > 0', async ({ page }) => {
    await gotoAndWait(page, { width: 1280, height: 800 });
    const count = await page.locator('#preact-session-list button[aria-expanded]').count();
    test.skip(count === 0, 'no fixture groups available — rely on structural test + Phase 8 fixtures');
    const zero = await findZeroWidthGroupTitles(page);
    expect(
      zero,
      `desktop: ${zero.length} group title spans have 0-px width — BUG #3.\n${formatHits(zero)}`,
    ).toEqual([]);
  });

  test('desktop 1280x800: at least one row has computed text-overflow ellipsis', async ({ page }) => {
    await gotoAndWait(page, { width: 1280, height: 800 });
    const rowCount = await page.locator('button[data-session-id], #preact-session-list button[aria-expanded]').count();
    test.skip(rowCount === 0, 'no fixture rows — ellipsis check deferred to Phase 8');
    const hasEllipsis = await hasEllipsisRow(page);
    expect(hasEllipsis, 'no row has computed text-overflow ellipsis — truncate utility is not active').toBe(true);
  });

  test('mobile 375x812: every session title span has width > 0', async ({ page }) => {
    await gotoAndWait(page, { width: 375, height: 812 });
    const count = await page.locator('button[data-session-id]').count();
    test.skip(count === 0, 'no fixture sessions on mobile — Phase 8 will add fixtures');
    const zero = await findZeroWidthSessionTitles(page);
    expect(
      zero,
      `mobile: ${zero.length} session title spans have 0-px width — BUG #3.\n${formatHits(zero)}`,
    ).toEqual([]);
  });

  // STRUCTURAL FALLBACK — always runs, fails before fix, passes after.
  // Reads SessionRow.js and GroupRow.js source and asserts the outer
  // button class string contains min-w-0 between w-full and flex items-center.
  // This is the gate that guarantees TDD ordering even if fixture sessions
  // don't render.
  test('structural: SessionRow.js outer button class contains min-w-0', () => {
    const p = join(__dirname, '..', '..', '..', 'internal', 'web', 'static', 'app', 'SessionRow.js');
    const src = readFileSync(p, 'utf-8');
    // Expect `group w-full min-w-0 ... flex items-center` on the outer button.
    // After WEB-P0-3 (plan 06-03) landed `relative` between `min-w-0` and
    // `flex items-center` to anchor the absolute-positioned action toolbar,
    // this regex allows optional positional utilities between min-w-0 and
    // flex. The load-bearing invariant for this BUG #3 / CRIT-03 gate is
    // that `min-w-0` is still present on the outer button class list.
    const outerButtonRe = /group w-full\s+min-w-0(?:\s+[-\w\[\]/:.]+)*\s+flex items-center/;
    expect(
      outerButtonRe.test(src),
      'SessionRow.js outer button class string is missing min-w-0 — BUG #3. Expected `group w-full min-w-0 [...] flex items-center` on the outer button.',
    ).toBe(true);
  });

  test('structural: GroupRow.js outer button class contains min-w-0', () => {
    const p = join(__dirname, '..', '..', '..', 'internal', 'web', 'static', 'app', 'GroupRow.js');
    const src = readFileSync(p, 'utf-8');
    const outerButtonRe = /group w-full\s+min-w-0\s+flex items-center/;
    expect(
      outerButtonRe.test(src),
      'GroupRow.js outer button class string is missing min-w-0 — BUG #3. Expected `group w-full min-w-0 flex items-center` on the outer button.',
    ).toBe(true);
  });
});
