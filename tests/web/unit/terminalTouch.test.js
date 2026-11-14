// unit/terminalTouch.test.js -- touch on the web terminal (#2680).
//
// On a touch device the terminal is not focused on mount, and xterm's own
// focus path (its mousedown handler) depends on compatibility mouse events
// that iOS drops for a tap once touchmove is cancelled. installTerminalTouch
// therefore focuses the terminal itself, synchronously in touchend, for a
// one-finger tap that stayed within TAP_SLOP_PX -- and never for a scroll
// drag, a multi-finger gesture or a read-only session.

import { describe, it, expect, vi } from 'vitest'

const modulePath = '../../../internal/web/static/app/terminalTouch.js'

// jsdom has no usable Touch constructor; the handlers only read
// touches[i].clientX/clientY and touches.length.
function touch(type, el, points) {
  const event = new Event(type, { bubbles: true, cancelable: true })
  Object.defineProperty(event, 'touches', { value: points.map(([x, y]) => ({ clientX: x, clientY: y })) })
  el.dispatchEvent(event)
  return event
}

async function setup({ readOnly = false } = {}) {
  const { installTerminalTouch, TAP_SLOP_PX } = await import(modulePath)
  const container = document.createElement('div')
  const xtermEl = document.createElement('div')
  container.appendChild(xtermEl)
  const focus = vi.fn()
  const wheel = vi.fn()
  xtermEl.addEventListener('wheel', wheel)
  const controller = new AbortController()
  installTerminalTouch(container, xtermEl, { focus, isReadOnly: () => readOnly, controller })
  return { container, xtermEl, focus, wheel, controller, TAP_SLOP_PX }
}

describe('installTerminalTouch', () => {
  it('focuses on a stationary one-finger tap', async () => {
    const { xtermEl, focus } = await setup()
    touch('touchstart', xtermEl, [[100, 100]])
    touch('touchend', xtermEl, [])
    expect(focus).toHaveBeenCalledTimes(1)
  })

  it('focuses on a tap with finger jitter inside the slop, and still cancels the touchmove', async () => {
    const { xtermEl, focus, TAP_SLOP_PX } = await setup()
    touch('touchstart', xtermEl, [[100, 100]])
    const move = touch('touchmove', xtermEl, [[101, 100 + TAP_SLOP_PX - 1]])
    touch('touchend', xtermEl, [])
    expect(move.defaultPrevented).toBe(true)
    expect(focus).toHaveBeenCalledTimes(1)
  })

  it('scrolls and does not focus on a drag past the slop', async () => {
    const { xtermEl, focus, wheel } = await setup()
    touch('touchstart', xtermEl, [[100, 200]])
    touch('touchmove', xtermEl, [[100, 170]])
    touch('touchmove', xtermEl, [[100, 120]])
    touch('touchend', xtermEl, [])
    expect(focus).not.toHaveBeenCalled()
    expect(wheel).toHaveBeenCalledTimes(2)
    expect(wheel.mock.calls[0][0].deltaY).toBe(30)
  })

  it('a drag that returns to its start is still a drag', async () => {
    const { xtermEl, focus } = await setup()
    touch('touchstart', xtermEl, [[100, 100]])
    touch('touchmove', xtermEl, [[100, 160]])
    touch('touchmove', xtermEl, [[100, 100]])
    touch('touchend', xtermEl, [])
    expect(focus).not.toHaveBeenCalled()
  })

  it('does not focus for a two-finger gesture, even after the fingers lift one by one', async () => {
    const { xtermEl, focus } = await setup()
    touch('touchstart', xtermEl, [[100, 100]])
    touch('touchstart', xtermEl, [[100, 100], [140, 140]])
    touch('touchend', xtermEl, [[100, 100]])
    touch('touchend', xtermEl, [])
    expect(focus).not.toHaveBeenCalled()
  })

  it('does not focus when the gesture is cancelled', async () => {
    const { xtermEl, focus } = await setup()
    touch('touchstart', xtermEl, [[100, 100]])
    touch('touchcancel', xtermEl, [])
    touch('touchend', xtermEl, [])
    expect(focus).not.toHaveBeenCalled()
  })

  it('does not focus a read-only session', async () => {
    const { xtermEl, focus } = await setup({ readOnly: true })
    touch('touchstart', xtermEl, [[100, 100]])
    touch('touchend', xtermEl, [])
    expect(focus).not.toHaveBeenCalled()
  })

  it('removes every listener on controller.abort()', async () => {
    const { xtermEl, focus, controller } = await setup()
    controller.abort()
    touch('touchstart', xtermEl, [[100, 100]])
    touch('touchend', xtermEl, [])
    expect(focus).not.toHaveBeenCalled()
  })
})

describe('installTerminalTouch: the tap\'s synthesized mousedown', () => {
  const mousedown = (el) => {
    const event = new MouseEvent('mousedown', { bubbles: true, cancelable: true })
    el.dispatchEvent(event)
    return event
  }

  it('is cancelled outside xterm right after a tap, so it cannot blur the focused terminal', async () => {
    const { container, xtermEl, focus } = await setup()
    touch('touchstart', xtermEl, [[100, 100]])
    touch('touchend', xtermEl, [])
    expect(focus).toHaveBeenCalledTimes(1)
    expect(mousedown(container).defaultPrevented).toBe(true)
  })

  it('is left alone over xterm, and when no tap preceded it', async () => {
    const { container, xtermEl } = await setup()
    expect(mousedown(container).defaultPrevented).toBe(false)
    touch('touchstart', xtermEl, [[100, 100]])
    touch('touchend', xtermEl, [])
    expect(mousedown(xtermEl).defaultPrevented).toBe(false)
  })
})
