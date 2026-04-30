import { test, expect } from '@playwright/test';
import { readFileSync } from 'fs';
import { join } from 'path';

/**
 * Issue #782 (JMBattista): when the underlying tmux session is gone, the
 * WebUI prints `[error:TMUX_SESSION_NOT_FOUND] tmux session is not
 * available` repeatedly to the terminal, with no actionable guidance.
 * The TUI surfaces a richer message ("session was lost; restart it from
 * the sidebar"); WebUI should match.
 *
 * Two-part fix:
 *   1. Frontend (TerminalPanel.js): on TMUX_SESSION_NOT_FOUND, render a
 *      single dismissable banner with actionable guidance instead of
 *      writing the same line to the terminal on every reconnect attempt.
 *      Also stop the auto-reconnect loop for this terminal-fatal code, so
 *      we don't print or retry in a tight cycle.
 *   2. Backend (handlers_ws.go): include a `hint` field with the
 *      actionable next step in the error payload, so the FE can render
 *      consistent guidance without hardcoding strings.
 */

const APP_ROOT = join(__dirname, '..', '..', '..', 'internal', 'web', 'static', 'app');
const REPO_ROOT = join(__dirname, '..', '..', '..');

test.describe('Issue #782 — disconnected-session error UX', () => {
  test('structural: handlers_ws.go emits a hint field on the TMUX_SESSION_NOT_FOUND error payload', () => {
    const src = readFileSync(join(REPO_ROOT, 'internal', 'web', 'handlers_ws.go'), 'utf-8');
    // The handler should set a Hint string when the code is
    // TMUX_SESSION_NOT_FOUND, so the FE can render actionable guidance
    // instead of an opaque error code.
    expect(
      /Hint\s*:/.test(src) || /"hint"/.test(src),
      'handlers_ws.go must populate a Hint/hint field on the error payload for actionable FE rendering — #782',
    ).toBe(true);
    expect(
      /TMUX_SESSION_NOT_FOUND[\s\S]{0,400}?(Hint|hint)/.test(src) ||
      /(Hint|hint)[\s\S]{0,400}?TMUX_SESSION_NOT_FOUND/.test(src),
      'handlers_ws.go must associate the Hint with the TMUX_SESSION_NOT_FOUND branch — #782',
    ).toBe(true);
  });

  test('structural: wsServerMessage in api_types.go (or sibling) declares a Hint field', () => {
    const candidates = ['handlers_ws.go', 'api_types.go', 'handlers.go'];
    let found = false;
    for (const name of candidates) {
      try {
        const src = readFileSync(join(REPO_ROOT, 'internal', 'web', name), 'utf-8');
        if (/Hint\s+string\s+`json:"hint/.test(src)) {
          found = true;
          break;
        }
      } catch { /* file may not exist; skip */ }
    }
    expect(
      found,
      'wsServerMessage struct (in handlers_ws.go or api_types.go) must declare a Hint field with json:"hint,omitempty" — #782',
    ).toBe(true);
  });

  test('structural: TerminalPanel.js stops auto-reconnect on TMUX_SESSION_NOT_FOUND (terminal-fatal)', () => {
    const src = readFileSync(join(APP_ROOT, 'TerminalPanel.js'), 'utf-8');
    // The fix sets ctx.wsReconnectEnabled = false when the error code is
    // TMUX_SESSION_NOT_FOUND so we don't repeatedly reconnect-fail-print
    // the same error.
    expect(
      /TMUX_SESSION_NOT_FOUND[\s\S]{0,400}?wsReconnectEnabled\s*=\s*false/.test(src),
      'TerminalPanel.js must set wsReconnectEnabled=false on TMUX_SESSION_NOT_FOUND so the reconnect loop does not spam the same error — #782',
    ).toBe(true);
  });

  test('structural: TerminalPanel.js renders a guidance banner (signal-driven) instead of writing the raw [error:CODE] line on every reconnect', () => {
    const src = readFileSync(join(APP_ROOT, 'TerminalPanel.js'), 'utf-8');
    // The fix introduces a useState for the terminal-fatal error so it
    // renders as a banner overlay (HTML), not just terminal.write().
    expect(
      /useState/.test(src),
      'TerminalPanel.js must use useState to hold a terminal-fatal error for banner rendering — #782',
    ).toBe(true);
    // The banner DOM should mention restart guidance.
    expect(
      /Restart|restart/.test(src),
      'TerminalPanel.js banner copy must include actionable guidance (e.g. "Restart") — #782',
    ).toBe(true);
  });

  test('structural: TerminalPanel.js does NOT write the same [error:...] line on every WS reconnect', () => {
    const src = readFileSync(join(APP_ROOT, 'TerminalPanel.js'), 'utf-8');
    // The original bug had `terminal.write('\r\n[error:' + ...)` fire on
    // every reconnect. After the fix, the terminal write should be
    // suppressed when a terminal-fatal banner is already showing — we
    // assert that the write happens behind a guard (e.g. !fatalError or
    // a code-aware branch).
    const rawWriteCount = (src.match(/terminal\.write\(\s*'\\r\\n\[error:/g) || []).length;
    expect(
      rawWriteCount,
      'TerminalPanel.js must collapse the raw `[error:` terminal write paths so a fatal session loss is shown ONCE in a banner, not on every reconnect — #782',
    ).toBeLessThanOrEqual(1);
  });
});
