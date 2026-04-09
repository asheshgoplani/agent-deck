import { test, expect } from '@playwright/test';
import { readFileSync } from 'fs';
import { join } from 'path';

/**
 * Phase 6 / Plan 05 / WEB-P0-4 prevention layer: mutations gating regression spec.
 *
 * When the web server is running with webMutations=false, the UI must hide
 * write controls (Stop/Restart/Fork/Delete buttons and the CreateSessionDialog)
 * so users cannot click them and generate 403 error spam. This spec asserts
 * the structural and DOM contract of that gating.
 *
 * Companion to 06-04 (toast cap / mitigation layer). 06-04 caps visible error
 * spam; 06-05 prevents it from being generated in the first place.
 */

const APP_DIR = join(__dirname, '..', '..', '..', 'internal', 'web', 'static', 'app');

function readApp(name: string): string {
  return readFileSync(join(APP_DIR, name), 'utf-8');
}

test.describe('WEB-P0-4 prevention layer — mutations gating', () => {
  test('structural: state.js exports mutationsEnabledSignal', () => {
    const src = readApp('state.js');
    expect(
      /export const mutationsEnabledSignal/.test(src),
      'state.js must export mutationsEnabledSignal (06-CONTEXT.md line 94).',
    ).toBe(true);
  });

  test('structural: AppShell.js fetches /api/settings and sets mutationsEnabledSignal', () => {
    const src = readApp('AppShell.js');
    expect(
      /fetch\(['"]\/api\/settings['"]/.test(src),
      'AppShell.js must fetch /api/settings to read webMutations (06-CONTEXT.md line 94).',
    ).toBe(true);
    expect(
      /mutationsEnabledSignal\.value\s*=/.test(src),
      'AppShell.js must assign mutationsEnabledSignal.value from the fetch response.',
    ).toBe(true);
  });

  test('structural: SessionRow.js reads mutationsEnabledSignal', () => {
    const src = readApp('SessionRow.js');
    expect(
      /mutationsEnabledSignal/.test(src),
      'SessionRow.js must import and read mutationsEnabledSignal to gate write buttons (06-CONTEXT.md line 94).',
    ).toBe(true);
  });

  test('structural: SessionRow.js has a conditional render gating the toolbar on mutationsEnabledSignal', () => {
    const src = readApp('SessionRow.js');
    // The toolbar wrap must be gated: look for `mutationsEnabled` short-circuit or `mutationsEnabledSignal.value` condition
    expect(
      /mutationsEnabled\s*&&\s*html/.test(src) || /mutationsEnabledSignal\.value\s*&&\s*html/.test(src),
      'SessionRow.js must wrap the `<div role="toolbar">` in a mutationsEnabled short-circuit so the toolbar does not render when mutations are disabled.',
    ).toBe(true);
  });

  test('SessionRow has a read-only lock indicator when mutations are disabled', () => {
    const src = readApp('SessionRow.js');
    expect(
      /!mutationsEnabledSignal\.value\s*&&\s*html/.test(src) || /!mutationsEnabled\s*&&\s*html/.test(src),
      'SessionRow.js must render a lock indicator when `!mutationsEnabledSignal.value` (or its local alias) — 06-CONTEXT.md line 94.',
    ).toBe(true);
  });

  test('structural: CreateSessionDialog.js imports mutationsEnabledSignal', () => {
    const src = readApp('CreateSessionDialog.js');
    expect(
      /mutationsEnabledSignal/.test(src),
      'CreateSessionDialog.js must import mutationsEnabledSignal to gate the create flow.',
    ).toBe(true);
  });

  test('structural: CreateSessionDialog.js early-returns null when mutations are disabled', () => {
    const src = readApp('CreateSessionDialog.js');
    expect(
      /if \(!mutationsEnabledSignal\.value\) return null/.test(src),
      'CreateSessionDialog.js must early-return null when `!mutationsEnabledSignal.value` (06-CONTEXT.md line 94).',
    ).toBe(true);
  });

  test('CreateSessionDialog submit button is disabled when mutations are off', () => {
    const src = readApp('CreateSessionDialog.js');
    expect(
      /disabled=\$\{[^}]*!mutationsEnabledSignal\.value/.test(src),
      'CreateSessionDialog.js submit button must be gated on `!mutationsEnabledSignal.value` as a belt-and-braces disable (06-CONTEXT.md line 94).',
    ).toBe(true);
  });

  test('DOM: when mutationsEnabledSignal=false, SessionRow toolbar buttons are absent', async ({ page }) => {
    await page.goto('/?t=test');
    await page.waitForSelector('header', { state: 'attached', timeout: 15000 });
    await page.waitForSelector('#preact-session-list', { state: 'attached', timeout: 15000 }).catch(() => {});
    const rowCount = await page.locator('button[data-session-id]').count();
    test.skip(rowCount === 0, 'no fixture sessions — cannot test SessionRow gating');
    await page.evaluate(async () => {
      const state: any = await import('/static/app/state.js');
      state.mutationsEnabledSignal.value = false;
    });
    await page.waitForTimeout(300);
    const buttonsInToolbar = await page.locator('button[data-session-id] [role="toolbar"] button').count();
    expect(
      buttonsInToolbar,
      'with mutationsEnabledSignal=false, no write buttons should render in any session toolbar',
    ).toBe(0);
  });

  test('DOM: when mutationsEnabledSignal=false, CreateSessionDialog does not render even when signaled open', async ({ page }) => {
    await page.goto('/?t=test');
    await page.waitForSelector('header', { state: 'attached', timeout: 15000 });
    await page.evaluate(async () => {
      const state: any = await import('/static/app/state.js');
      state.mutationsEnabledSignal.value = false;
      // Try to open the dialog programmatically
      if (state.createSessionDialogSignal) {
        state.createSessionDialogSignal.value = true;
      }
    });
    await page.waitForTimeout(300);
    // The dialog should not be in the DOM. Test looks for any dialog with the create-session marker.
    const dialogCount = await page.locator('[role="dialog"][aria-label*="Create"]').count()
      + await page.locator('form[data-testid="create-session-form"]').count();
    expect(dialogCount, 'CreateSessionDialog must not render when mutationsEnabledSignal is false').toBe(0);
  });

  test('DOM: non-regression — when mutationsEnabledSignal=true, SessionRow toolbar and CreateSessionDialog ARE rendered', async ({ page }) => {
    await page.goto('/?t=test');
    await page.waitForSelector('header', { state: 'attached', timeout: 15000 });
    await page.waitForSelector('#preact-session-list', { state: 'attached', timeout: 15000 }).catch(() => {});
    await page.evaluate(async () => {
      const state: any = await import('/static/app/state.js');
      state.mutationsEnabledSignal.value = true;
    });
    await page.waitForTimeout(300);
    const rowCount = await page.locator('button[data-session-id]').count();

    // Non-regression check 1: when mutations are on, the read-only lock indicator must NOT render
    const lockCount = await page.locator('button[data-session-id] [aria-label="Read-only"]').count();
    expect(
      lockCount,
      'with mutationsEnabledSignal=true, no row should render the Read-only lock indicator',
    ).toBe(0);

    // Non-regression check 2: if fixtures exist, the toolbar wrapper must render on every row
    if (rowCount > 0) {
      const toolbarCount = await page.locator('button[data-session-id] [role="toolbar"]').count();
      expect(
        toolbarCount,
        'with mutationsEnabledSignal=true, every session row must render a <div role="toolbar">',
      ).toBeGreaterThanOrEqual(1);
    }

    // Non-regression check 3: CreateSessionDialog opens normally when mutations are on
    await page.evaluate(async () => {
      const state: any = await import('/static/app/state.js');
      state.createSessionDialogSignal.value = true;
    });
    await page.waitForTimeout(300);
    // Dialog renders via the "New Session" heading plus a submit button
    const newSessionHeadingCount = await page.getByRole('heading', { name: /new session/i }).count();
    expect(
      newSessionHeadingCount,
      'with mutationsEnabledSignal=true, CreateSessionDialog must render when opened',
    ).toBeGreaterThanOrEqual(1);
    // Cleanup: close the dialog so other tests start clean
    await page.evaluate(async () => {
      const state: any = await import('/static/app/state.js');
      state.createSessionDialogSignal.value = false;
    });
  });
});
