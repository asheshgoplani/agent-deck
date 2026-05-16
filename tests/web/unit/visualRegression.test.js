// Self-tests for helpers/visualRegression.js. These exercise the pure
// functions (name builder, options builder) without needing a real
// Playwright runtime. Real-browser integration is exercised by
// e2e/ specs that import the helper.

import { describe, it, expect, vi } from 'vitest'

import {
  DEFAULT_VOLATILE_SELECTORS,
  stateScreenshotName,
  stateScreenshotOptions,
  snapshotState,
} from '../helpers/visualRegression.js'

describe('stateScreenshotName', () => {
  it('joins element + state with double-dash', () => {
    expect(stateScreenshotName('status-pill', 'running')).toBe(
      'status-pill--running.png',
    )
  })

  it('appends a variant when present', () => {
    expect(stateScreenshotName('badge', 'idle', 'dark')).toBe(
      'badge--idle--dark.png',
    )
  })

  it('sanitizes path-unsafe characters', () => {
    expect(stateScreenshotName('foo/bar', 'a b', 'x.y')).toBe(
      'foo-bar--a-b--x-y.png',
    )
  })

  it('throws when element or state is missing', () => {
    expect(() => stateScreenshotName('', 'running')).toThrow(/required/)
    expect(() => stateScreenshotName('foo', '')).toThrow(/required/)
  })
})

describe('stateScreenshotOptions', () => {
  // Minimal page stub — we only need a `locator` factory.
  const fakePage = () => ({
    locator: vi.fn((sel) => ({ __selector: sel })),
  })

  it('masks every default volatile selector', () => {
    const page = fakePage()
    const opts = stateScreenshotOptions(page)
    expect(opts.mask).toHaveLength(DEFAULT_VOLATILE_SELECTORS.length)
    const used = page.locator.mock.calls.map((c) => c[0])
    for (const sel of DEFAULT_VOLATILE_SELECTORS) {
      expect(used).toContain(sel)
    }
  })

  it('appends extra mask selectors and dedupes against defaults', () => {
    const page = fakePage()
    const extras = ['[data-testid="custom"]', '[data-volatile]']
    stateScreenshotOptions(page, { maskSelectors: extras })
    const used = page.locator.mock.calls.map((c) => c[0])
    // The duplicate '[data-volatile]' must appear only once.
    const dupCount = used.filter((s) => s === '[data-volatile]').length
    expect(dupCount).toBe(1)
    expect(used).toContain('[data-testid="custom"]')
  })

  it('honors maxDiffPixelRatio when provided', () => {
    const page = fakePage()
    const opts = stateScreenshotOptions(page, { maxDiffPixelRatio: 0.05 })
    expect(opts.maxDiffPixelRatio).toBe(0.05)
  })

  it('omits maxDiffPixelRatio when not provided', () => {
    const page = fakePage()
    const opts = stateScreenshotOptions(page, {})
    expect(opts.maxDiffPixelRatio).toBeUndefined()
  })

  it('always disables animations and hides caret', () => {
    const page = fakePage()
    const opts = stateScreenshotOptions(page)
    expect(opts.animations).toBe('disabled')
    expect(opts.caret).toBe('hide')
  })

  it('forwards passthrough options', () => {
    const page = fakePage()
    const opts = stateScreenshotOptions(page, {
      passthrough: { fullPage: true, scale: 'css' },
    })
    expect(opts.fullPage).toBe(true)
    expect(opts.scale).toBe('css')
  })
})

describe('snapshotState', () => {
  it('calls setState then expects toHaveScreenshot with built name', async () => {
    const setState = vi.fn(async () => {})
    const target = {
      waitFor: vi.fn(async () => {}),
    }
    const page = { locator: vi.fn(() => target) }
    const toHaveScreenshot = vi.fn(async () => {})
    const expectFn = vi.fn(() => ({ toHaveScreenshot }))

    await snapshotState({
      page,
      expect: expectFn,
      element: 'badge',
      state: 'running',
      locator: '[data-testid="status"]',
      setState,
    })

    expect(setState).toHaveBeenCalledOnce()
    expect(target.waitFor).toHaveBeenCalledWith({ state: 'visible' })
    expect(toHaveScreenshot).toHaveBeenCalled()
    const [name] = toHaveScreenshot.mock.calls[0]
    expect(name).toBe('badge--running.png')
  })

  it('throws when setState is not a function', async () => {
    await expect(() =>
      snapshotState({
        page: {},
        expect: () => ({}),
        element: 'x',
        state: 'y',
        locator: '.z',
      }),
    ).rejects.toThrow(/function/)
  })
})
