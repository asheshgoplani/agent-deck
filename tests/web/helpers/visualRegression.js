// helpers/visualRegression.js -- screenshot-regression helper for
// state-derived UI elements (TEST-PLAN.md §6.5).
//
// Most of our visual baselines (visual-baselines.spec.js) capture
// whole-page renders. State-derived elements (status pills, hook
// indicators, cost badges) need a different protocol:
//
//   1. Drive the app into a known state via /__fixture endpoints.
//   2. Mask volatile content (clocks, ephemeral counters) so the
//      diff is meaningful.
//   3. Snapshot a specific element by data-testid / role, not the
//      whole page — so a redesign that doesn't touch the indicator
//      doesn't churn the baseline.
//
// Self-test lives in helpers/visualRegression.spec.js (vitest); the
// real Playwright integration runs in e2e/ specs that import this.

/**
 * Default selectors masked on every state snapshot.
 *
 * These are content elements whose value depends on wall-clock time or
 * other ambient state we don't want to assert in pixel form.
 */
export const DEFAULT_VOLATILE_SELECTORS = Object.freeze([
  '[data-volatile]',
  'time',
  '[data-testid="last-updated"]',
  '[data-testid="cost-running-total"]',
])

/**
 * Build the snapshot file name for a state-derived element.
 *
 * The format encodes both the element under test and the named state
 * so the file path stays readable in the screenshots/ tree.
 *
 *   stateScreenshotName('status-pill', 'running') -> 'status-pill--running.png'
 *   stateScreenshotName('badge', 'idle', 'dark')  -> 'badge--idle--dark.png'
 *
 * @param {string} element  - logical name of the element under test (no path)
 * @param {string} state    - named state ('running', 'idle', 'overflow', ...)
 * @param {string} [variant] - optional variant suffix ('dark', 'compact', ...)
 */
export function stateScreenshotName(element, state, variant) {
  if (!element || !state) {
    throw new Error('visualRegression: element and state are required')
  }
  const safe = (s) => String(s).replace(/[^a-z0-9_-]/gi, '-')
  const parts = variant ? [element, state, variant] : [element, state]
  return parts.map(safe).join('--') + '.png'
}

/**
 * Build the options object for `expect(locator).toHaveScreenshot(name, opts)`.
 *
 * Includes the default volatile masks plus any extras the caller passes,
 * dedup'd. Other options pass through.
 *
 * @param {import('@playwright/test').Page} page  - Playwright page
 * @param {object} [opts]
 * @param {string[]} [opts.maskSelectors] - extra CSS selectors to mask
 * @param {number} [opts.maxDiffPixelRatio] - override the global ratio
 * @param {object} [opts.passthrough] - extra options forwarded verbatim
 */
export function stateScreenshotOptions(page, opts = {}) {
  const selectors = dedupe([
    ...DEFAULT_VOLATILE_SELECTORS,
    ...(opts.maskSelectors || []),
  ])
  const mask = selectors.map((sel) => page.locator(sel))
  const out = { mask, animations: 'disabled', caret: 'hide' }
  if (typeof opts.maxDiffPixelRatio === 'number') {
    out.maxDiffPixelRatio = opts.maxDiffPixelRatio
  }
  if (opts.passthrough) {
    Object.assign(out, opts.passthrough)
  }
  return out
}

/**
 * Snapshot a single state-derived element.
 *
 * Drives the app into the named state via `setState`, masks volatile
 * elements, and asserts toHaveScreenshot against the deterministic
 * baseline name.
 *
 * @param {object} args
 * @param {import('@playwright/test').Page} args.page
 * @param {import('@playwright/test').Expect} args.expect - Playwright's expect
 * @param {string} args.element     - logical element name for the snapshot file
 * @param {string} args.state       - named state ('running', etc.)
 * @param {string} [args.variant]   - optional variant suffix
 * @param {string} args.locator     - CSS selector or role for the element
 * @param {() => Promise<void>} args.setState - drives the app into args.state
 * @param {object} [args.options]   - forwarded to stateScreenshotOptions
 */
export async function snapshotState({
  page,
  expect,
  element,
  state,
  variant,
  locator,
  setState,
  options,
}) {
  if (typeof setState !== 'function') {
    throw new Error('visualRegression: setState must be a function')
  }
  await setState()
  const target = page.locator(locator)
  await target.waitFor({ state: 'visible' })
  const name = stateScreenshotName(element, state, variant)
  const opts = stateScreenshotOptions(page, options)
  await expect(target).toHaveScreenshot(name, opts)
}

function dedupe(arr) {
  return Array.from(new Set(arr))
}
