// Phase 8 plan 03 — PERF bundle regression spec.
//
// Eight structural assertions proving the six PERF requirements shipped
// together:
//   PERF-B: Chart.js <script> tag has the `defer` attribute
//   PERF-C: vendor/addon-canvas.js is deleted (dead in xterm v6)
//   PERF-D: TerminalPanel injects <link rel="preload"> for addon-webgl
//   PERF-F: SessionList consumes a new useDebounced hook
//   PERF-G: SessionRow is wrapped in memo(); GroupRow holds local collapse
//           state via useState
//   PERF-I: SessionList POSTs /api/costs/batch with JSON body; handlers_costs.go
//           gained a POST route
//
// All assertions are readFileSync / existsSync based — no running server
// required. The spec exists solely to pin the structural surface so future
// edits can't silently undo the performance fixes.

import { test, expect } from '@playwright/test';
import { readFileSync, existsSync } from 'fs';
import { join } from 'path';

const ROOT = join(__dirname, '..', '..', '..');
const INDEX_HTML = join(ROOT, 'internal', 'web', 'static', 'index.html');
const SESSION_LIST = join(ROOT, 'internal', 'web', 'static', 'app', 'SessionList.js');
const SESSION_ROW = join(ROOT, 'internal', 'web', 'static', 'app', 'SessionRow.js');
const GROUP_ROW = join(ROOT, 'internal', 'web', 'static', 'app', 'GroupRow.js');
const TERMINAL_PANEL = join(ROOT, 'internal', 'web', 'static', 'app', 'TerminalPanel.js');
const USE_DEBOUNCED = join(ROOT, 'internal', 'web', 'static', 'app', 'useDebounced.js');
const ADDON_CANVAS = join(ROOT, 'internal', 'web', 'static', 'vendor', 'addon-canvas.js');
const HANDLERS_COSTS = join(ROOT, 'internal', 'web', 'handlers_costs.go');

test.describe('PERF Bundle — Phase 8 plan 3 regression', () => {
  test('PERF-B: Chart.js script tag has defer attribute', () => {
    const src = readFileSync(INDEX_HTML, 'utf-8');
    expect(
      /<script\s+src="\/static\/chart\.umd\.min\.js"\s+defer\s*><\/script>/.test(src),
      'index.html must load chart.umd.min.js with the defer attribute (PERF-B). Expected: <script src="/static/chart.umd.min.js" defer></script>',
    ).toBe(true);
  });

  test('PERF-C: vendor/addon-canvas.js has been deleted', () => {
    expect(
      existsSync(ADDON_CANVAS),
      'internal/web/static/vendor/addon-canvas.js must not exist (PERF-C — dead code in xterm v6)',
    ).toBe(false);
  });

  test('PERF-D: TerminalPanel injects a preload link for the WebGL addon', () => {
    const src = readFileSync(TERMINAL_PANEL, 'utf-8');
    const hasPreloadLink = /rel=['"]preload['"]/.test(src);
    expect(
      hasPreloadLink,
      'TerminalPanel.js must include a <link rel="preload"> for the WebGL addon (PERF-D, via programmatic injection or static markup). Dynamic import() is rejected per Pitfall 5.',
    ).toBe(true);
    expect(
      /addon-webgl/.test(src),
      'TerminalPanel.js must reference addon-webgl in the preload link.',
    ).toBe(true);
  });

  test('PERF-F: SessionList uses the new useDebounced hook', () => {
    const src = readFileSync(SESSION_LIST, 'utf-8');
    expect(
      /useDebounced/.test(src),
      'SessionList.js must import and use useDebounced for the search input (PERF-F, 250ms debounce).',
    ).toBe(true);
    expect(
      existsSync(USE_DEBOUNCED),
      'internal/web/static/app/useDebounced.js must exist as a new helper hook (~15 lines).',
    ).toBe(true);
  });

  test('PERF-I: costs batch uses POST with JSON body (not GET query string)', () => {
    const src = readFileSync(SESSION_LIST, 'utf-8');
    // Old pattern must be gone
    expect(
      /\/api\/costs\/batch\?ids=/.test(src),
      'SessionList.js must NOT use GET /api/costs/batch?ids=... — replaced with POST (PERF-I).',
    ).toBe(false);
    // New pattern must be present: fetch('/api/costs/batch', { method: 'POST', ... })
    const fetchMatch = /fetch\(['"]\/api\/costs\/batch['"],?\s*\{[^}]*method:\s*['"]POST['"]/s;
    expect(
      fetchMatch.test(src),
      "SessionList.js must call fetch('/api/costs/batch', { method: 'POST', ... }) (PERF-I).",
    ).toBe(true);
  });

  test('PERF-G SessionRow: wrapped in memo()', () => {
    const src = readFileSync(SESSION_ROW, 'utf-8');
    expect(
      /\bmemo\s*\(/.test(src),
      'SessionRow.js must wrap its export in memo() (PERF-G).',
    ).toBe(true);
    // And memo must be imported from preact/compat or equivalent
    const importsMemo = /import\s*\{[^}]*\bmemo\b[^}]*\}\s*from\s*['"][^'"]*preact[^'"]*['"]/.test(src);
    expect(
      importsMemo,
      'SessionRow.js must import memo from preact/compat (or equivalent preact module path).',
    ).toBe(true);
  });

  test('PERF-G GroupRow: holds local collapse state via useState', () => {
    const src = readFileSync(GROUP_ROW, 'utf-8');
    expect(
      /\buseState\s*\(/.test(src),
      'GroupRow.js must call useState() to hold its own collapse state.',
    ).toBe(true);
    // At least one of the conventional collapse-state identifiers must appear
    const hasCollapseState = /\b(isOpen|collapsed|expanded|isCollapsed|isExpanded)\b/.test(src);
    expect(
      hasCollapseState,
      'GroupRow.js must declare a local collapse state (isOpen, collapsed, expanded, isCollapsed, or isExpanded).',
    ).toBe(true);
  });

  test('PERF-I backend: handlers_costs.go has a POST /api/costs/batch route', () => {
    const src = readFileSync(HANDLERS_COSTS, 'utf-8');
    const hasPost = /http\.MethodPost|"POST"/.test(src);
    const hasRoute = /costs\/batch/.test(src);
    expect(
      hasPost && hasRoute,
      'handlers_costs.go must add a POST handler for /api/costs/batch (PERF-I backend).',
    ).toBe(true);
  });
});
