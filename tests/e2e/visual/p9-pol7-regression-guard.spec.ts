import { test, expect } from '@playwright/test'
import { readFileSync } from 'fs'
import { join } from 'path'

/**
 * Phase 9 / Plan 03 / POL-7 regression guard
 *
 * POL-7 was shipped in Phase 6 plan 04 (commits d3b4f35, aa1c974, a7f2548,
 * cf8322e). This spec asserts that the invariants documented in
 * `.planning/phases/09-polish/09-03-POL-7-TRACEABILITY.md` remain intact
 * in the current source. A future refactor that removes any of these
 * strings will fail this spec before it can silently break POL-7.
 *
 * All assertions are source-level (readFileSync) — no web server boot,
 * no DOM navigation. This keeps the guard fast and runnable anywhere.
 */

const ROOT = join(__dirname, '..', '..', '..')
const TOAST_JS = readFileSync(join(ROOT, 'internal/web/static/app/Toast.js'), 'utf8')
const DRAWER_JS = readFileSync(join(ROOT, 'internal/web/static/app/ToastHistoryDrawer.js'), 'utf8')
const STATE_JS = readFileSync(join(ROOT, 'internal/web/static/app/state.js'), 'utf8')

test.describe('POL-7 regression guard — Toast eviction', () => {
  test('Toast.js caps visible stack at 3 via next.length > 3', () => {
    expect(TOAST_JS).toMatch(/next\.length\s*>\s*3/)
  })

  test('Toast.js has setTimeout branch for auto-dismiss', () => {
    expect(TOAST_JS).toMatch(/setTimeout/)
  })

  test('Toast.js splits ARIA live region by severity (role="alert" + role="status")', () => {
    expect(TOAST_JS).toMatch(/role="alert"/)
    expect(TOAST_JS).toMatch(/aria-live="assertive"/)
    expect(TOAST_JS).toMatch(/role="status"/)
    expect(TOAST_JS).toMatch(/aria-live="polite"/)
  })
})

test.describe('POL-7 regression guard — History drawer', () => {
  test('ToastHistoryDrawer exports both ToastHistoryDrawer and ToastHistoryDrawerToggle', () => {
    expect(DRAWER_JS).toMatch(/export\s+function\s+ToastHistoryDrawer\b/)
    expect(DRAWER_JS).toMatch(/export\s+function\s+ToastHistoryDrawerToggle\b/)
  })

  test('ToastHistoryDrawer dialog has role="dialog" and aria-modal="true"', () => {
    expect(DRAWER_JS).toMatch(/role="dialog"/)
    expect(DRAWER_JS).toMatch(/aria-modal="true"/)
  })

  test('Toggle has data-testid="toast-history-toggle"', () => {
    expect(DRAWER_JS).toMatch(/data-testid="toast-history-toggle"/)
  })

  test('Toggle has a 44x44 touch target (min-w-[44px] min-h-[44px] or equivalent)', () => {
    expect(DRAWER_JS).toMatch(/(min-w-\[44px\][\s\S]*min-h-\[44px\]|w-11[\s\S]*h-11)/)
  })
})

test.describe('POL-7 regression guard — state.js signals', () => {
  test('state.js exports toastHistorySignal', () => {
    expect(STATE_JS).toMatch(/export\s+const\s+toastHistorySignal\b/)
  })

  test('state.js exports toastHistoryOpenSignal', () => {
    expect(STATE_JS).toMatch(/export\s+const\s+toastHistoryOpenSignal\b/)
  })

  test('state.js persists toast history to localStorage key agentdeck_toast_history', () => {
    expect(STATE_JS).toMatch(/agentdeck_toast_history/)
  })
})
