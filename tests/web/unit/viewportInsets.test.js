// unit/viewportInsets.test.js -- viewportInsets publishes the phone-visible
// viewport geometry into the two CSS custom properties styles.src.css already
// consumes on `.app`:
//
//     height: var(--app-height);
//     padding-bottom: calc(var(--keyboard-inset) + env(safe-area-inset-bottom));
//
// Both variables were declared in :root and never written by any module, so
// `--app-height` stayed at its `100vh` default and `--keyboard-inset` at `0px`.
// On iOS Safari `100vh` is the *large* viewport (toolbars hidden), so with the
// toolbars showing the app box is taller than the screen and the bottom row --
// the mobile tab bar -- is pushed under the fold. When the software keyboard
// opens, nothing moves, so the caret sits behind the keyboard.
//
// `window.innerHeight` tracks the toolbars on iOS where `100vh` does not, and
// `visualViewport` is what reports the keyboard. This module is the missing
// producer for those two variables.

import { describe, it, expect } from 'vitest'

const modulePath = '../../../internal/web/static/app/viewportInsets.js'

// A stand-in for `window` carrying only what the module reads. Listener
// registration is recorded so the install test can fire events itself.
const fakeWindow = ({ innerHeight, visual }) => {
  const listeners = []
  const visualViewport = visual
    ? {
        height: visual.height,
        offsetTop: visual.offsetTop ?? 0,
        addEventListener: (type, fn, opts) => listeners.push({ on: 'visual', type, fn, opts }),
      }
    : undefined
  return {
    innerHeight,
    visualViewport,
    addEventListener: (type, fn, opts) => listeners.push({ on: 'window', type, fn, opts }),
    listeners,
  }
}

const load = () => import(modulePath)

const apply = async (win, root) => {
  const { applyViewportInsets } = await load()
  return applyViewportInsets(win, root)
}

describe('applyViewportInsets', () => {
  it('publishes the layout viewport height as --app-height', async () => {
    const root = document.createElement('div')

    await apply(fakeWindow({ innerHeight: 812, visual: { height: 812 } }), root)

    expect(root.style.getPropertyValue('--app-height')).toBe('812px')
  })

  it('publishes the height the software keyboard covers as --keyboard-inset', async () => {
    const root = document.createElement('div')
    // iPhone 13: 812pt layout viewport, keyboard leaves 476pt visible.
    const win = fakeWindow({ innerHeight: 812, visual: { height: 476, offsetTop: 0 } })

    await apply(win, root)

    expect(root.style.getPropertyValue('--keyboard-inset')).toBe('336px')
  })

  it('subtracts the visual viewport offset so a scrolled-up page is not counted twice', async () => {
    const root = document.createElement('div')
    // iOS scrolls the visual viewport when the keyboard opens over a focused
    // field near the bottom; offsetTop is that shift, not keyboard height.
    const win = fakeWindow({ innerHeight: 812, visual: { height: 476, offsetTop: 100 } })

    await apply(win, root)

    expect(root.style.getPropertyValue('--keyboard-inset')).toBe('236px')
  })

  it('never publishes a negative inset when iOS overscroll reports a taller visual viewport', async () => {
    const root = document.createElement('div')
    const win = fakeWindow({ innerHeight: 812, visual: { height: 900, offsetTop: 0 } })

    await apply(win, root)

    expect(root.style.getPropertyValue('--keyboard-inset')).toBe('0px')
  })

  it('falls back to the layout viewport with no inset when visualViewport is absent', async () => {
    const root = document.createElement('div')

    await apply(fakeWindow({ innerHeight: 640, visual: null }), root)

    expect(root.style.getPropertyValue('--app-height')).toBe('640px')
    expect(root.style.getPropertyValue('--keyboard-inset')).toBe('0px')
  })
})

describe('installViewportInsets', () => {
  it('republishes the inset when the visual viewport resizes', async () => {
    const { installViewportInsets } = await load()
    const root = document.createElement('div')
    const win = fakeWindow({ innerHeight: 812, visual: { height: 812, offsetTop: 0 } })

    installViewportInsets(win, root, new AbortController().signal)
    expect(root.style.getPropertyValue('--keyboard-inset')).toBe('0px')

    // The keyboard opens: the browser shrinks the visual viewport, then fires.
    win.visualViewport.height = 476
    const resize = win.listeners.find((l) => l.on === 'visual' && l.type === 'resize')
    resize.fn()

    expect(root.style.getPropertyValue('--keyboard-inset')).toBe('336px')
  })

  it('registers every listener with the caller\'s abort signal so teardown is automatic', async () => {
    const { installViewportInsets } = await load()
    const root = document.createElement('div')
    const win = fakeWindow({ innerHeight: 812, visual: { height: 812, offsetTop: 0 } })
    const { signal } = new AbortController()

    installViewportInsets(win, root, signal)

    expect(win.listeners.length).toBeGreaterThan(0)
    for (const l of win.listeners) {
      expect(l.opts?.signal).toBe(signal)
    }
  })
})

describe('applyViewportInsets in a viewport that has no size yet', () => {
  // A background tab, a bfcache restore, and the moment before first layout all
  // report innerHeight 0. Publishing that flattens `.app` to zero height -- it is sized
  // by --app-height -- so the whole UI disappears. Holding the previous value
  // keeps the CSS `100dvh` fallback in charge until a real measurement arrives.
  it('leaves the published values alone when the viewport reports zero height', async () => {
    const root = document.createElement('div')
    root.style.setProperty('--app-height', '812px')
    root.style.setProperty('--keyboard-inset', '284px')

    await apply(fakeWindow({ innerHeight: 0, visual: { height: 0, offsetTop: 0 } }), root)

    expect(root.style.getPropertyValue('--app-height')).toBe('812px')
    expect(root.style.getPropertyValue('--keyboard-inset')).toBe('284px')
  })
})
