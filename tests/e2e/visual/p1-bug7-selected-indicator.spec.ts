import { test, expect } from '@playwright/test';
import { readFileSync } from 'fs';
import { join } from 'path';

/**
 * Phase 3 / Plan 03 / Task 1: BUG #7 / LAYT-04 regression test
 *
 * Asserts that the currently selected SessionRow is visually distinct from
 * unselected and hovered siblings:
 *   - computed border-left-width === '4px'
 *   - computed border-left-color is not transparent
 *   - computed background-color differs from unselected and hovered-unselected
 *   - aria-current === 'true' on the outer button (screen reader affordance)
 *
 * Root cause (LOCKED per 03-CONTEXT.md): SessionRow.js line 87 uses
 * border-l-2 + dark:bg-tn-blue/25 bg-blue-100 on the selected branch. The
 * 2px bar is too subtle; the tint can merge with hovered rows depending on
 * theme.
 *
 * Fix (LOCKED per 03-CONTEXT.md): bump the base outer button border to
 * border-l-4, keep the selected branch's border-tn-blue color, bump the
 * dark tint to dark:bg-tn-blue/30, keep bg-blue-100 for light, and add
 * aria-current=${isSelected ? 'true' : 'false'} on the outer button.
 *
 * TDD ORDER: committed in failing state in Task 1, flipped to green in Task 2.
 *
 * STRUCTURAL FALLBACK: four file-read tests always run — two positive
 * (border-l-4 + dark:bg-tn-blue/30 + aria-current present) and one negative
 * (border-l-2 gone from outer button).
 */

const SESSION_ROW_PATH = join(
  __dirname, '..', '..', '..', 'internal', 'web', 'static', 'app', 'SessionRow.js',
);

function readSrc(): string {
  return readFileSync(SESSION_ROW_PATH, 'utf-8');
}

test.describe('BUG #7 / LAYT-04 — selected session visual indicator', () => {
  // STRUCTURAL TESTS — always run, fail before fix, pass after.

  test('structural: SessionRow.js has border-l-4 on outer button', () => {
    const src = readSrc();
    // Outer button class must contain border-l-4 (selector uses any border style).
    // Plan 06-03 inserted `relative` between min-w-0 and `flex items-center`
    // to anchor the absolute-positioned action toolbar, so this regex
    // permits optional positional utilities between them.
    const re = /class="group w-full min-w-0(?:\s+[-\w\[\]/:.]+)*\s+flex items-center[\s\S]*?border-l-4/;
    expect(
      re.test(src),
      'SessionRow.js outer button class must include border-l-4. LAYT-04 bumps the selected-row accent bar from 2px to 4px for visible distinction.',
    ).toBe(true);
  });

  test('structural: SessionRow.js no longer has border-l-2 on outer button', () => {
    const src = readSrc();
    const re = /class="group w-full min-w-0(?:\s+[-\w\[\]/:.]+)*\s+flex items-center[\s\S]*?border-l-2(\s|"|\${)/;
    expect(
      re.test(src),
      'SessionRow.js outer button still has border-l-2. LAYT-04 replaces border-l-2 with border-l-4 (same width on selected AND unselected rows, differing only by color and tint).',
    ).toBe(false);
  });

  test('structural: SessionRow.js selected branch has dark:bg-tn-blue/30 tint', () => {
    const src = readSrc();
    expect(
      /dark:bg-tn-blue\/30/.test(src),
      'SessionRow.js selected branch must use dark:bg-tn-blue/30 (stronger than the previous /25) for a clearly distinct tint against hovered-unselected rows.',
    ).toBe(true);
  });

  test('structural: SessionRow.js outer button has aria-current attribute', () => {
    const src = readSrc();
    const re = /aria-current=\$\{isSelected \? 'true' : 'false'\}/;
    expect(
      re.test(src),
      'SessionRow.js outer button must set aria-current=${isSelected ? \'true\' : \'false\'} for screen-reader affordance per LAYT-04.',
    ).toBe(true);
  });

  // RUNTIME TESTS — skip without fixture sessions; prove computed styles.

  test('runtime: selected session has border-left-width === 4px and non-transparent color', async ({ page }) => {
    await page.setViewportSize({ width: 1280, height: 800 });
    await page.goto('/?t=test');
    await page.waitForSelector('header', { state: 'attached', timeout: 15000 });
    await page.waitForSelector('#preact-session-list', { state: 'attached', timeout: 15000 }).catch(() => {});

    const count = await page.locator('#preact-session-list button[data-session-id]').count();
    test.skip(count === 0, 'no fixture sessions — structural tests cover the gate');

    await page.locator('#preact-session-list button[data-session-id]').first().click();
    // Wait for aria-current to flip (runs after isSelected state propagates).
    await page.waitForFunction(
      () => {
        const btns = document.querySelectorAll('#preact-session-list button[data-session-id]');
        for (let i = 0; i < btns.length; i++) {
          if (btns[i].getAttribute('aria-current') === 'true') return true;
        }
        return false;
      },
      null,
      { timeout: 5000 },
    ).catch(() => {});

    const style = await page.evaluate(() => {
      const btn = document.querySelector('#preact-session-list button[data-session-id][aria-current="true"]') as HTMLElement | null;
      if (!btn) return null;
      const cs = window.getComputedStyle(btn);
      return {
        borderLeftWidth: cs.borderLeftWidth,
        borderLeftColor: cs.borderLeftColor,
        backgroundColor: cs.backgroundColor,
      };
    });

    expect(style, 'no selected button found with aria-current="true"').not.toBe(null);
    expect(style!.borderLeftWidth, `border-left-width must be 4px, got ${style!.borderLeftWidth}`).toBe('4px');
    expect(
      style!.borderLeftColor === 'rgba(0, 0, 0, 0)' || style!.borderLeftColor === 'transparent',
      `border-left-color must not be transparent, got ${style!.borderLeftColor}`,
    ).toBe(false);
    expect(
      style!.backgroundColor === 'rgba(0, 0, 0, 0)' || style!.backgroundColor === 'transparent',
      `background-color must not be transparent, got ${style!.backgroundColor}`,
    ).toBe(false);
  });

  test('runtime: selected background differs from unselected sibling background', async ({ page }) => {
    await page.setViewportSize({ width: 1280, height: 800 });
    await page.goto('/?t=test');
    await page.waitForSelector('header', { state: 'attached', timeout: 15000 });
    await page.waitForSelector('#preact-session-list', { state: 'attached', timeout: 15000 }).catch(() => {});

    const count = await page.locator('#preact-session-list button[data-session-id]').count();
    test.skip(count < 2, 'need at least two fixture sessions to compare selected vs unselected backgrounds');

    await page.locator('#preact-session-list button[data-session-id]').first().click();
    await page.waitForTimeout(200);

    const bgs = await page.evaluate(() => {
      const btns = document.querySelectorAll('#preact-session-list button[data-session-id]');
      if (btns.length < 2) return null;
      const selected = btns[0] as HTMLElement;
      const unselected = btns[1] as HTMLElement;
      return {
        selected: window.getComputedStyle(selected).backgroundColor,
        unselected: window.getComputedStyle(unselected).backgroundColor,
      };
    });

    expect(bgs, 'need two buttons to compare').not.toBe(null);
    expect(
      bgs!.selected,
      `selected backgroundColor (${bgs!.selected}) must differ from unselected (${bgs!.unselected})`,
    ).not.toBe(bgs!.unselected);
  });

  test('runtime: selected background differs from hovered-unselected background', async ({ page }) => {
    await page.setViewportSize({ width: 1280, height: 800 });
    await page.goto('/?t=test');
    await page.waitForSelector('header', { state: 'attached', timeout: 15000 });
    await page.waitForSelector('#preact-session-list', { state: 'attached', timeout: 15000 }).catch(() => {});

    const count = await page.locator('#preact-session-list button[data-session-id]').count();
    test.skip(count < 2, 'need at least two fixture sessions for hover comparison');

    await page.locator('#preact-session-list button[data-session-id]').first().click();
    await page.waitForTimeout(200);
    await page.locator('#preact-session-list button[data-session-id]').nth(1).hover();
    await page.waitForTimeout(150);

    const bgs = await page.evaluate(() => {
      const btns = document.querySelectorAll('#preact-session-list button[data-session-id]');
      if (btns.length < 2) return null;
      const selected = btns[0] as HTMLElement;
      const hovered = btns[1] as HTMLElement;
      return {
        selected: window.getComputedStyle(selected).backgroundColor,
        hovered: window.getComputedStyle(hovered).backgroundColor,
      };
    });

    expect(bgs, 'need two buttons for hover comparison').not.toBe(null);
    expect(
      bgs!.selected,
      `selected backgroundColor (${bgs!.selected}) must differ from hovered-unselected (${bgs!.hovered})`,
    ).not.toBe(bgs!.hovered);
  });

  test('runtime: selected button has aria-current="true"', async ({ page }) => {
    await page.setViewportSize({ width: 1280, height: 800 });
    await page.goto('/?t=test');
    await page.waitForSelector('header', { state: 'attached', timeout: 15000 });
    await page.waitForSelector('#preact-session-list', { state: 'attached', timeout: 15000 }).catch(() => {});

    const count = await page.locator('#preact-session-list button[data-session-id]').count();
    test.skip(count === 0, 'no fixture sessions');

    await page.locator('#preact-session-list button[data-session-id]').first().click();
    await page.waitForTimeout(200);

    const current = await page.locator('#preact-session-list button[data-session-id]').first().getAttribute('aria-current');
    expect(
      current,
      'selected button must have aria-current="true" for screen readers',
    ).toBe('true');
  });
});
