import { test, expect } from '@playwright/test';
import { readFileSync } from 'fs';
import { join } from 'path';

/**
 * Issue #783 (JMBattista): WebUI hover icon placement.
 *
 * Repro: hovering a session row reveals an absolute-positioned action
 * toolbar (`absolute right-2 top-1/2 -translate-y-1/2`). The same row
 * also renders a `tool` label (e.g. "shell") and an optional cost badge
 * in normal flex flow on the right. When the toolbar appears it sits on
 * top of those flow-rendered elements — the screenshot in the issue
 * shows the trash icon overlapping the "shell" text.
 *
 * Fix: when the toolbar is visible, hide the tool label + cost badge so
 * the hover state does not produce visual overlap. Use the same
 * `(hovered || focused || isSelected || hasFocusWithin) && mutationsEnabled`
 * predicate that already gates the toolbar visibility.
 */

const APP_ROOT = join(__dirname, '..', '..', '..', 'internal', 'web', 'static', 'app');

test.describe('Issue #783 — hover toolbar overlap', () => {
  test('structural: SessionRow.js hides the tool label when the action toolbar is visible', () => {
    const src = readFileSync(join(APP_ROOT, 'SessionRow.js'), 'utf-8');
    // The fix introduces a `toolbarVisible` predicate (or equivalent inline
    // expression) and uses it both to gate the toolbar AND to hide the
    // tool/cost spans. We assert that the tool label span is conditional
    // on a "not toolbarVisible" expression.
    const toolLabelSection = src.match(/text-xs[^"`]*?dark:text-tn-muted[^"`]*?text-gray-600[\s\S]{0,400}?session\.tool/);
    expect(
      toolLabelSection != null,
      'SessionRow.js must still render a `session.tool` label span (regression check)',
    ).toBe(true);
    // Heuristic: the fix wraps the metadata cluster in a flex span that has
    // a conditional class hiding it on hover/focus/selected. We accept any
    // of: `hidden` toggled in, an `invisible` toggle, or a conditional
    // `display: none` style. Easiest: a `toolbarVisible` flag used to set
    // `hidden`/`invisible` on the metadata span.
    const hidesMetadataOnHover =
      /toolbarVisible\s*\?\s*'(hidden|invisible)'/.test(src) ||
      /toolbarVisible\s*&&\s*'(hidden|invisible)'/.test(src) ||
      /!toolbarVisible[^?]*?\?\s*'/.test(src);
    expect(
      hidesMetadataOnHover,
      'SessionRow.js must hide the tool label + cost badge while the action toolbar is visible (e.g. via a `toolbarVisible` flag) — #783',
    ).toBe(true);
  });

  test('structural: SessionRow.js declares a single `toolbarVisible` predicate reused by both the metadata cluster and the toolbar', () => {
    const src = readFileSync(join(APP_ROOT, 'SessionRow.js'), 'utf-8');
    expect(
      /const\s+toolbarVisible\s*=/.test(src),
      'SessionRow.js must declare a `toolbarVisible` const so both the toolbar visibility AND the metadata-hide use the same predicate (avoids drift) — #783',
    ).toBe(true);
  });

  test('structural: action toolbar still uses the absolute-positioned right-anchored layout (regression check on layout invariants)', () => {
    const src = readFileSync(join(APP_ROOT, 'SessionRow.js'), 'utf-8');
    expect(
      /absolute\s+right-2\s+top-1\/2\s+-translate-y-1\/2/.test(src),
      'SessionRow.js toolbar must remain absolute right-anchored (the fix is to hide the metadata, not to relayout the toolbar)',
    ).toBe(true);
  });
});
