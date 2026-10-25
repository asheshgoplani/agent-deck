// unit/terminalTouch.test.js -- touch handling and tap-to-focus for mobile terminal.
// Verifies touch scrolling, stationary single-finger tap-to-focus, and two-finger gesture guards.

import { describe, it, expect, vi } from 'vitest'

const modulePath = '../../../internal/web/static/app/TerminalPanel.js'

function createFakeTouch(x, y) {
  return { clientX: x, clientY: y }
}

function dispatchTouchEvent(element, type, touches) {
  const event = new Event(type, { bubbles: true, cancelable: true })
  Object.defineProperty(event, 'touches', { value: touches })
  element.dispatchEvent(event)
  return event
}

describe('installTouchScroll', () => {
  it('focuses the terminal on a stationary single-finger tap', async () => {
    const { installTouchScroll } = await import(modulePath)
    const container = document.createElement('div')
    const xtermEl = document.createElement('div')
    const terminal = { focus: vi.fn() }
    const controller = new AbortController()

    installTouchScroll(container, xtermEl, terminal, controller)

    // Touch start with single finger
    dispatchTouchEvent(container, 'touchstart', [createFakeTouch(100, 100)])
    // Touch end: finger lifted
    dispatchTouchEvent(container, 'touchend', [])

    expect(terminal.focus).toHaveBeenCalledTimes(1)
  })

  it('does not focus the terminal when touch moved beyond threshold (scroll/drag)', async () => {
    const { installTouchScroll } = await import(modulePath)
    const container = document.createElement('div')
    const xtermEl = document.createElement('div')
    const terminal = { focus: vi.fn() }
    const controller = new AbortController()

    installTouchScroll(container, xtermEl, terminal, controller)

    dispatchTouchEvent(container, 'touchstart', [createFakeTouch(100, 100)])
    // Dragged by 10px vertically (threshold is 6px)
    dispatchTouchEvent(container, 'touchmove', [createFakeTouch(100, 110)])
    dispatchTouchEvent(container, 'touchend', [])

    expect(terminal.focus).not.toHaveBeenCalled()
  })

  it('clears active state when a two-finger gesture starts and does not focus on touchend', async () => {
    const { installTouchScroll } = await import(modulePath)
    const container = document.createElement('div')
    const xtermEl = document.createElement('div')
    const terminal = { focus: vi.fn() }
    const controller = new AbortController()

    installTouchScroll(container, xtermEl, terminal, controller)

    // First finger down
    dispatchTouchEvent(container, 'touchstart', [createFakeTouch(100, 100)])
    // Second finger down (pinch/two-finger gesture)
    dispatchTouchEvent(container, 'touchstart', [createFakeTouch(100, 100), createFakeTouch(150, 150)])
    // First finger lifted, one finger remains
    dispatchTouchEvent(container, 'touchend', [createFakeTouch(150, 150)])

    expect(terminal.focus).not.toHaveBeenCalled()

    // Second finger lifted, all touches ended
    dispatchTouchEvent(container, 'touchend', [])

    expect(terminal.focus).not.toHaveBeenCalled()
  })

  it('does not focus when lifting one finger if touches remain (touches.length !== 0)', async () => {
    const { installTouchScroll } = await import(modulePath)
    const container = document.createElement('div')
    const xtermEl = document.createElement('div')
    const terminal = { focus: vi.fn() }
    const controller = new AbortController()

    installTouchScroll(container, xtermEl, terminal, controller)

    // Single touch
    dispatchTouchEvent(container, 'touchstart', [createFakeTouch(100, 100)])
    // Touchend still reports one touch remaining
    dispatchTouchEvent(container, 'touchend', [createFakeTouch(100, 100)])

    expect(terminal.focus).not.toHaveBeenCalled()
  })

  it('resets state on touchcancel without focusing terminal', async () => {
    const { installTouchScroll } = await import(modulePath)
    const container = document.createElement('div')
    const xtermEl = document.createElement('div')
    const terminal = { focus: vi.fn() }
    const controller = new AbortController()

    installTouchScroll(container, xtermEl, terminal, controller)

    // Touch started then cancelled by browser/system gesture
    dispatchTouchEvent(container, 'touchstart', [createFakeTouch(100, 100)])
    dispatchTouchEvent(container, 'touchcancel', [])

    expect(terminal.focus).not.toHaveBeenCalled()

    // Subsequent touchend without active touch does not focus
    dispatchTouchEvent(container, 'touchend', [])
    expect(terminal.focus).not.toHaveBeenCalled()
  })
})
