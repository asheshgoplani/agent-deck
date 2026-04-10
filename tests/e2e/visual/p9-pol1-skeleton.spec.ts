import { test, expect } from '@playwright/test';
import { readFileSync } from 'fs';
import { join } from 'path';

/**
 * Phase 9 / Plan 01 / Task 1: POL-1 regression test
 *
 * POL-1 (skeleton loader): the sidebar renders a skeleton placeholder stack
 * while the first /api/menu response or SSE `menu` snapshot is still in
 * flight. The skeleton uses Tailwind's `animate-pulse` utility and honors
 * `prefers-reduced-motion` via `motion-reduce:animate-none`.
 *
 * Contract:
 *   1. state.js exports `sessionsLoadedSignal = signal(false)` at the TAIL
 *      (after `mutationsEnabledSignal`, per the 06-05 STATE.md handoff rule).
 *   2. main.js flips `sessionsLoadedSignal.value = true` on BOTH the
 *      /api/menu loadMenu path AND the SSE `menu` event handler.
 *   3. SessionList.js imports sessionsLoadedSignal and, when the signal
 *      value is false, returns a `<ul data-testid="sidebar-skeleton">`
 *      with placeholder rows that carry `animate-pulse
 *      motion-reduce:animate-none`.
 *   4. DOM: with /api/menu route-intercepted (permanently stalled), the
 *      skeleton is visible. Once the real menu settles, the skeleton is
 *      gone.
 *
 * TDD ORDER: this spec is committed in FAILING state in Task 1, then
 * Task 2 lands the state.js + main.js + SessionList.js edits, flipping
 * the spec to green.
 */

const STATE_JS_PATH = join(
  __dirname, '..', '..', '..',
  'internal', 'web', 'static', 'app', 'state.js',
);
const MAIN_JS_PATH = join(
  __dirname, '..', '..', '..',
  'internal', 'web', 'static', 'app', 'main.js',
);
const SESSION_LIST_PATH = join(
  __dirname, '..', '..', '..',
  'internal', 'web', 'static', 'app', 'SessionList.js',
);

test.describe('POL-1 — sidebar skeleton loader', () => {
  // ===== Layer 1: structural (always runs) =====

  test('structural: state.js exports sessionsLoadedSignal = signal(false)', () => {
    const src = readFileSync(STATE_JS_PATH, 'utf-8');
    const re = /export\s+const\s+sessionsLoadedSignal\s*=\s*signal\(\s*false\s*\)/;
    expect(
      re.test(src),
      'state.js must export `sessionsLoadedSignal = signal(false)` so the sidebar skeleton knows when to replace itself with real data.',
    ).toBe(true);
  });

  test('structural: sessionsLoadedSignal is appended AFTER mutationsEnabledSignal (06-05 tail rule)', () => {
    const src = readFileSync(STATE_JS_PATH, 'utf-8');
    const lines = src.split('\n');
    let mutIdx = -1;
    let loadedIdx = -1;
    for (let i = 0; i < lines.length; i++) {
      if (mutIdx === -1 && /\bmutationsEnabledSignal\b/.test(lines[i])) mutIdx = i;
      if (loadedIdx === -1 && /\bsessionsLoadedSignal\b/.test(lines[i])) loadedIdx = i;
    }
    expect(mutIdx, 'mutationsEnabledSignal must be present in state.js').toBeGreaterThan(-1);
    expect(loadedIdx, 'sessionsLoadedSignal must be present in state.js').toBeGreaterThan(-1);
    expect(
      loadedIdx,
      `sessionsLoadedSignal (line ${loadedIdx}) must appear AFTER mutationsEnabledSignal (line ${mutIdx}) — per STATE.md 06-05 handoff, new signals are APPENDED AT THE TAIL.`,
    ).toBeGreaterThan(mutIdx);
  });

  test('structural: main.js imports sessionsLoadedSignal and flips it in BOTH loadMenu + SSE handler', () => {
    const src = readFileSync(MAIN_JS_PATH, 'utf-8');
    // Import check: must import sessionsLoadedSignal from ./state.js (same import block or separate line)
    const importRe = /sessionsLoadedSignal[\s\S]{0,300}from\s*['"]\.\/state\.js['"]/;
    expect(
      importRe.test(src),
      'main.js must import sessionsLoadedSignal from ./state.js',
    ).toBe(true);
    // At least two `.value = true` flips (one per loadMenu, one per SSE snapshot path)
    const flipMatches = src.match(/sessionsLoadedSignal\.value\s*=\s*true/g) || [];
    expect(
      flipMatches.length,
      `main.js must flip sessionsLoadedSignal.value = true at least twice (loadMenu + SSE handler). Found ${flipMatches.length} occurrences.`,
    ).toBeGreaterThanOrEqual(2);
  });

  test('structural: SessionList.js imports sessionsLoadedSignal and renders the skeleton placeholder', () => {
    const src = readFileSync(SESSION_LIST_PATH, 'utf-8');
    expect(
      /sessionsLoadedSignal/.test(src),
      'SessionList.js must import sessionsLoadedSignal',
    ).toBe(true);
    expect(
      /sessionsLoadedSignal\.value/.test(src),
      'SessionList.js must read sessionsLoadedSignal.value in the render body',
    ).toBe(true);
    expect(
      /data-testid="sidebar-skeleton"/.test(src),
      'SessionList.js must render a <ul data-testid="sidebar-skeleton"> during the loading phase',
    ).toBe(true);
    expect(
      /animate-pulse/.test(src) && /motion-reduce:animate-none/.test(src),
      'SessionList.js skeleton placeholder rows must carry `animate-pulse` and `motion-reduce:animate-none` (POL-1 a11y requirement).',
    ).toBe(true);
  });

  // ===== Layer 2: DOM =====

  test('DOM: skeleton renders while /api/menu is stalled', async ({ page }) => {
    // Stall /api/menu indefinitely so the skeleton branch is the only render
    // path that can possibly fire. We also stall SSE `/events/menu` so the
    // SSE handler can't backfill the signal.
    await page.route('**/api/menu*', () => {
      // Never fulfill — the fetch promise hangs
    });
    await page.route('**/events/menu*', () => {
      // Never fulfill — SSE stream stalls before open
    });
    await page.goto('/?token=test');
    // Wait for the skeleton to appear (Preact mount + first render)
    const skeleton = page.locator('[data-testid="sidebar-skeleton"]');
    await expect(skeleton).toBeVisible({ timeout: 3000 });
    const placeholderCount = await skeleton.locator('li').count();
    expect(
      placeholderCount,
      `skeleton should render at least 6 placeholder rows (plan says 8 is sensible); got ${placeholderCount}`,
    ).toBeGreaterThanOrEqual(6);
  });

  test('DOM: skeleton disappears once the real menu has settled', async ({ page }) => {
    await page.goto('/?token=test');
    // Wait for the skeleton to be gone (real menu arrived OR empty-state took over)
    await page.waitForFunction(
      () => !document.querySelector('[data-testid="sidebar-skeleton"]'),
      null,
      { timeout: 8000 },
    );
    // Either the real list ul OR the "No sessions" empty state must be present
    const listCount = await page.locator('#preact-session-list').count();
    const emptyText = await page.getByText(/No( matching)? sessions/i).count();
    expect(
      listCount > 0 || emptyText > 0,
      'after the skeleton clears, SessionList must render either #preact-session-list or the "No sessions" empty state',
    ).toBe(true);
  });
});
