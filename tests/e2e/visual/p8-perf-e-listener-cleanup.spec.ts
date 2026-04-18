import { test, expect } from '@playwright/test';
import { readFileSync } from 'fs';
import { join } from 'path';

/**
 * Phase 8 / Plan 02 / Task 1: PERF-E regression test (TerminalPanel listener leak).
 *
 * CURRENT STATE (verified by grep of TerminalPanel.js as of 2026-04-09):
 *   - 8 addEventListener sites inside the main useEffect:
 *     - 4 container touch listeners (touchstart, touchmove, touchend, touchcancel)
 *     - 1 window resize listener (already uses a LOCAL windowResizeController -- partially done)
 *     - 4 ws.addEventListener (open, message, error, close)
 *   - The mobile-only anonymous touchstart preventDefault was removed when
 *     mobile console input was enabled; that dropped the site count from 9 to 8.
 *   - Only 1 of these currently uses controller.signal (the window resize block).
 *   - The existing cleanup at line 67 only manually removes the first touchstart; the
 *     remaining 7 listeners leak on every unmount / reconnect.
 *
 * FIX TO ENFORCE (PERF-E):
 *   - ONE new AbortController() declared at the top of the useEffect.
 *   - Every addEventListener call carries { signal: controller.signal } in its options.
 *   - The effect cleanup calls controller.abort() exactly once; manual
 *     removeEventListener pairs for the migrated listeners are deleted.
 *
 * The assertions below are structural (readFileSync only), which means:
 *   1. They fail immediately against the current broken TerminalPanel.js (only 1
 *      controller.signal occurrence exists).
 *   2. They guarantee the fix shipped in Task 2 cannot regress without the spec
 *      flipping back to red -- no running server or WebGL mock required.
 */

const TERMINAL_PANEL_PATH = join(
  __dirname,
  '..',
  '..',
  '..',
  'internal',
  'web',
  'static',
  'app',
  'TerminalPanel.js',
);

function source(): string {
  return readFileSync(TERMINAL_PANEL_PATH, 'utf-8');
}

test.describe('PERF-E -- TerminalPanel listener cleanup via AbortController', () => {
  test('structural: declares at least one new AbortController()', () => {
    const src = source();
    expect(
      src.includes('new AbortController()'),
      'TerminalPanel.js must declare new AbortController() inside the useEffect to group listener cleanup.',
    ).toBe(true);
  });

  test('structural: contains controller.signal at least 8 times (one per addEventListener site)', () => {
    const src = source();
    const matches = src.match(/controller\.signal/g) || [];
    expect(
      matches.length,
      `Expected controller.signal to appear on every addEventListener site (>=8), found ${matches.length}. Sites: 4 touch on container + 1 window resize + 4 ws. (Mobile-only touchstart preventDefault was removed when mobile input was enabled.)`,
    ).toBeGreaterThanOrEqual(8);
  });

  test('structural: contains controller.abort() in the effect cleanup', () => {
    const src = source();
    expect(
      src.includes('controller.abort()'),
      'TerminalPanel.js useEffect cleanup must call controller.abort() to detach every listener in the group.',
    ).toBe(true);
  });

  test('structural: no bare addEventListener call lacking a signal option', () => {
    const src = source();
    const re = /addEventListener\(/g;
    const offenders: number[] = [];
    let m: RegExpExecArray | null;
    while ((m = re.exec(src)) !== null) {
      const idx = m.index;
      const windowText = src.slice(idx, idx + 300);
      // Must contain signal: within 300 chars of the call. We accept
      // signal identifier reuse (e.g. { signal: ctrl.signal }) via the
      // literal signal: prefix in the options object.
      if (!/signal\s*:/.test(windowText)) {
        offenders.push(idx);
      }
    }
    expect(
      offenders,
      `Bare addEventListener calls without signal option found at byte offsets: ${offenders.join(', ')}. Every addEventListener must pass { signal: controller.signal } per PERF-E.`,
    ).toEqual([]);
  });

  test('structural: no manual removeEventListener for the 8 migrated listeners', () => {
    const src = source();
    // The AbortController pattern replaces manual removeEventListener.
    // If any removeEventListener for touch* events remains, the cleanup
    // is half-migrated and the test must fail.
    const hasManualRemove = /removeEventListener\(\s*['"]touch(start|move|end|cancel)['"]/.test(src);
    expect(
      hasManualRemove,
      'TerminalPanel.js must NOT manually removeEventListener for touch events; controller.abort() handles them.',
    ).toBe(false);
  });

  test('structural: all 4 ws.addEventListener calls have signal option', () => {
    const src = source();
    const wsRe = /ws\.addEventListener\(\s*['"](open|message|error|close)['"]/g;
    const events = ['open', 'message', 'error', 'close'];
    const seen = new Set<string>();
    let m: RegExpExecArray | null;
    while ((m = wsRe.exec(src)) !== null) {
      const eventName = m[1];
      const windowText = src.slice(m.index, m.index + 500);
      expect(
        /signal\s*:/.test(windowText),
        `ws.addEventListener('${eventName}') at byte ${m.index} lacks signal option in the next 500 chars.`,
      ).toBe(true);
      seen.add(eventName);
    }
    for (const ev of events) {
      expect(
        seen.has(ev),
        `ws.addEventListener('${ev}') not found -- expected 4 WebSocket listeners (open, message, error, close).`,
      ).toBe(true);
    }
  });
});
