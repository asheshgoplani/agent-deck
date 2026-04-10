import { test, expect } from '@playwright/test';
import { readFileSync } from 'fs';
import { join } from 'path';

/**
 * Phase 6 / Plan 01 / Task 1: WEB-P0-2 regression test (Option B read-only label)
 *
 * Backend constraint (verified during 06-CONTEXT.md gathering): server.go:79
 * binds cfg.Profile ONCE at NewServer() time. There is no per-request profile
 * resolver, so Option A (`?profile=X` reload) is infeasible without
 * re-architecting the profile isolation model (explicitly OUT OF SCOPE per
 * REQUIREMENTS.md line 121).
 *
 * Option B — locked fix (CONTEXT.md lines 57-69):
 *   - When profiles.length <= 1: render a non-interactive `<div role="status">`
 *     label. Screen readers announce it as status text, not a disabled button.
 *   - When profiles.length > 1: the dropdown remains as a listbox, but each
 *     option is non-interactive (no onClick) and the help text
 *     `Switch profiles by restarting agent-deck with -p <name>` is ALWAYS
 *     visible when the dropdown is open (not gated on profiles.length > 1).
 *
 * TDD gate: this spec is committed in FAILING state in Task 1 (current
 * ProfileDropdown.js lacks role="status", uses conditional help text, etc.),
 * then Task 2 implements Option B so the spec flips to green.
 *
 * STRUCTURAL FALLBACK: the first four tests readFileSync against the source
 * file so the TDD contract is verifiable even if the test server has no
 * profiles endpoint. The remaining two DOM tests exercise the live render to
 * guard against "source says one thing, browser does another" regressions.
 */

const PROFILE_DROPDOWN_PATH = join(
  __dirname,
  '..',
  '..',
  '..',
  'internal',
  'web',
  'static',
  'app',
  'ProfileDropdown.js',
);

test.describe('WEB-P0-2 — ProfileDropdown is Option B (read-only label)', () => {
  test('structural: ProfileDropdown.js source contains role="status" (single-profile read-only path)', () => {
    const src = readFileSync(PROFILE_DROPDOWN_PATH, 'utf-8');
    expect(
      /role="status"/.test(src),
      'ProfileDropdown.js must render a role="status" element for the single-profile case per WEB-P0-2 Option B (CONTEXT.md line 64).',
    ).toBe(true);
  });

  test('structural: help text is always-visible inside the dropdown when open', () => {
    const src = readFileSync(PROFILE_DROPDOWN_PATH, 'utf-8');
    expect(
      src.includes('Switch profiles by restarting agent-deck with -p'),
      'ProfileDropdown.js must contain the literal help text `Switch profiles by restarting agent-deck with -p <name>` per CONTEXT.md line 66.',
    ).toBe(true);
  });

  test('structural: profile option rows have NO onClick handler (non-interactive)', () => {
    const src = readFileSync(PROFILE_DROPDOWN_PATH, 'utf-8');
    // Sanity: the file must still render role="option" rows.
    const hasRoleOption = /role="option"/.test(src);
    expect(hasRoleOption, 'sanity: file must still render role="option" rows').toBe(true);
    // Scan a 500-char window after the first role="option" occurrence to
    // catch an onClick that is part of the option row (rather than an
    // unrelated handler elsewhere in the component).
    const optionIdx = src.indexOf('role="option"');
    const window = src.slice(optionIdx, optionIdx + 500);
    expect(
      /onClick/.test(window),
      'role="option" rows must NOT have onClick handlers — Option B is read-only.',
    ).toBe(false);
  });

  test('structural: no ?profile= query-string construction anywhere', () => {
    const src = readFileSync(PROFILE_DROPDOWN_PATH, 'utf-8');
    expect(
      /\?profile=/.test(src),
      'ProfileDropdown.js must NOT construct ?profile= URLs — Option A is infeasible per server.go:79 (CONTEXT.md line 61).',
    ).toBe(false);
  });

  test('DOM: current profile name is visible in the topbar area', async ({ page }) => {
    await page.goto('/?t=test');
    await page.waitForSelector('header', { state: 'attached', timeout: 15000 });
    const resp = await page.request.get('/api/profiles?t=test');
    if (!resp.ok()) test.skip(true, 'profiles endpoint unreachable');
    const data = await resp.json();
    const current = String(data.current || 'default');
    // Give the dropdown component a moment to mount and hydrate after fetch.
    await page.waitForTimeout(500);
    const headerText = await page.locator('header').innerText();
    expect(
      headerText.includes(current),
      `header must display the current profile "${current}"; got: ${headerText.slice(0, 200)}`,
    ).toBe(true);
  });

  test('DOM: clicking a non-current option does NOT navigate or change current', async ({ page }) => {
    await page.goto('/?t=test');
    await page.waitForSelector('header', { state: 'attached', timeout: 15000 });
    const resp = await page.request.get('/api/profiles?t=test');
    if (!resp.ok()) test.skip(true, 'profiles endpoint unreachable');
    const data = await resp.json();
    const profiles: string[] = data.profiles || [];
    test.skip(
      profiles.length < 2,
      'need at least 2 profiles for this case; single-profile is covered by structural test 1',
    );
    await page.waitForTimeout(500);
    // Open the dropdown if the trigger button is present. If the component
    // rendered the read-only status element instead, this click is a no-op.
    await page
      .locator('[aria-haspopup="listbox"], [role="status"]')
      .first()
      .click()
      .catch(() => { /* read-only status will not open */ });
    const urlBefore = page.url();
    const nonCurrent = profiles.find((p) => p !== data.current);
    if (nonCurrent) {
      await page
        .getByRole('option', { name: nonCurrent })
        .first()
        .click({ trial: false, force: true })
        .catch(() => { /* non-interactive */ });
      await page.waitForTimeout(300);
    }
    expect(page.url(), 'clicking a non-current profile option must not navigate').toBe(urlBefore);
  });
});
