import { test, expect } from '@playwright/test';
import { readFileSync } from 'fs';
import { join } from 'path';

/**
 * Issue #784 (JMBattista): ConfirmDialog does not steal focus from the
 * Delete button on the row that opened it.
 *
 * Repro:
 *   1. Click a row's Delete button. Browser focus stays on that button.
 *   2. ConfirmDialog opens. Its Cancel button has `autofocus`, but Preact
 *      does NOT honor the `autofocus` attribute on already-mounted
 *      components in htm/preact (it is only respected by the browser at
 *      initial document parse for the FIRST autofocus element).
 *   3. User hits Enter expecting "Cancel" — instead, focus is still on the
 *      sidebar Delete button, the dialog dismisses, and the row's delete
 *      action re-fires, re-opening the dialog. Delete loop.
 *
 * Fix:
 *   - Replace `autofocus` with useRef + useEffect to programmatically
 *     focus the Cancel button on mount.
 *   - Wrap the dialog body in a `role="dialog"` `aria-modal="true"` element
 *     and add an Enter handler so pressing Enter activates the focused
 *     button rather than escaping back to the row.
 *   - Esc dismissal already lives in useKeyboardNav.js (closes the
 *     confirmDialogSignal); this fix preserves that path.
 */

const APP_ROOT = join(__dirname, '..', '..', '..', 'internal', 'web', 'static', 'app');

test.describe('Issue #784 — ConfirmDialog focus trap', () => {
  test('structural: ConfirmDialog.js does NOT use the autofocus attribute (broken in Preact mid-render)', () => {
    const src = readFileSync(join(APP_ROOT, 'ConfirmDialog.js'), 'utf-8');
    expect(
      /\sautofocus(\s|>)/.test(src),
      'ConfirmDialog.js must NOT rely on autofocus — Preact only honors it at initial document parse, so the row-level Delete button keeps focus and Enter re-opens the dialog (#784)',
    ).toBe(false);
  });

  test('structural: ConfirmDialog.js imports useRef and useEffect from preact/hooks', () => {
    const src = readFileSync(join(APP_ROOT, 'ConfirmDialog.js'), 'utf-8');
    expect(
      /from\s+'preact\/hooks'/.test(src),
      'ConfirmDialog.js must import from preact/hooks to use useRef + useEffect',
    ).toBe(true);
    expect(
      /useRef/.test(src),
      'ConfirmDialog.js must use useRef to hold the focus target',
    ).toBe(true);
    expect(
      /useEffect/.test(src),
      'ConfirmDialog.js must use useEffect to programmatically focus on mount',
    ).toBe(true);
  });

  test('structural: ConfirmDialog.js calls .focus() inside an effect', () => {
    const src = readFileSync(join(APP_ROOT, 'ConfirmDialog.js'), 'utf-8');
    expect(
      /\.focus\(\)/.test(src),
      'ConfirmDialog.js must call .focus() (typically on cancelRef.current) so the Cancel button receives focus when the dialog mounts',
    ).toBe(true);
  });

  test('structural: ConfirmDialog.js wires an Enter key handler so Enter activates the focused dialog button (not the underlying row)', () => {
    const src = readFileSync(join(APP_ROOT, 'ConfirmDialog.js'), 'utf-8');
    // Either an onKeyDown checking 'Enter' on the dialog container, or the
    // focused <button> being the activation target with native Enter handling
    // (which works once focus is correctly inside the dialog).
    const hasKeyHandler =
      /onKeyDown/.test(src) || /'Enter'/.test(src) || /"Enter"/.test(src);
    expect(
      hasKeyHandler,
      'ConfirmDialog.js must handle the Enter key (or be structured so native button activation handles it once focus is correct) — see #784 root cause',
    ).toBe(true);
  });

  test('structural: ConfirmDialog.js exposes role="dialog" + aria-modal so screen readers + key routing match the visual modality', () => {
    const src = readFileSync(join(APP_ROOT, 'ConfirmDialog.js'), 'utf-8');
    expect(
      /role="dialog"/.test(src),
      'ConfirmDialog.js must declare role="dialog" on the modal panel for a11y and key-routing correctness',
    ).toBe(true);
    expect(
      /aria-modal="true"/.test(src),
      'ConfirmDialog.js must declare aria-modal="true" on the modal panel',
    ).toBe(true);
  });
});
