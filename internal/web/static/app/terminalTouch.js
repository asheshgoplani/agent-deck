// terminalTouch.js -- touch handling for the terminal container on phones
// and tablets: one-finger drag scrolls, a one-finger tap focuses (#2680).
//
// Why the tap needs its own focus path: on a touch device TerminalPanel does
// not focus the terminal on mount (that would pop the keyboard on every
// session switch), so focus was left to xterm's mousedown handler, which runs
// only for the compatibility mouse events the browser synthesizes after a
// tap. Those do not reliably arrive on iOS: the drag-to-scroll handler below
// must cancel touchmove, and a real fingertip nearly always moves a pixel or
// two during a tap, which makes Safari drop the synthesized mousedown/click.
// iOS also only raises the keyboard for a focus() made inside the user's
// gesture, so the focus happens synchronously in touchend.

// A finger that stays within this many CSS pixels of where it landed is a
// tap, not a drag.
export const TAP_SLOP_PX = 10

// How long after a tap its synthesized mousedown may still arrive.
const TAP_MOUSEDOWN_WINDOW_MS = 1000

// installTerminalTouch wires the handlers onto container (capture phase, so
// they run before xterm's own listeners). xtermEl receives the synthesized
// wheel events that scroll the terminal. focus() is called for a tap unless
// isReadOnly() is true. Every listener registers on controller.signal, so a
// single controller.abort() removes them.
export function installTerminalTouch(container, xtermEl, { focus, isReadOnly = () => false, controller }) {
  if (!container || !xtermEl) return

  let scrolling = false // one finger down: drag scrolls
  let tapCandidate = false // still a tap: one finger, no travel past the slop
  let startX = 0
  let startY = 0
  let lastY = 0
  let tapFocusedAt = -Infinity

  function onTouchStart(event) {
    const touches = event.touches
    if (!touches || touches.length !== 1) {
      // A second finger turns the whole gesture into a pinch/multi-touch:
      // neither a scroll nor a tap, even after the extra finger lifts.
      scrolling = false
      tapCandidate = false
      return
    }
    scrolling = true
    tapCandidate = true
    startX = touches[0].clientX
    startY = lastY = touches[0].clientY
  }

  function onTouchMove(event) {
    const touches = event.touches
    if (!scrolling || !touches || touches.length !== 1) return
    event.preventDefault()
    const x = touches[0].clientX
    const y = touches[0].clientY
    if (Math.abs(x - startX) > TAP_SLOP_PX || Math.abs(y - startY) > TAP_SLOP_PX) tapCandidate = false
    const delta = lastY - y
    lastY = y
    if (delta !== 0) {
      xtermEl.dispatchEvent(new WheelEvent('wheel', {
        deltaY: delta,
        deltaMode: 0,
        bubbles: true,
        cancelable: true,
      }))
    }
  }

  function onTouchEnd(event) {
    const remaining = event.touches ? event.touches.length : 0
    if (remaining > 0) return // wait for the last finger
    const wasTap = tapCandidate
    scrolling = false
    tapCandidate = false
    if (wasTap && !isReadOnly()) {
      focus()
      tapFocusedAt = Date.now()
    }
  }

  // The tap's synthesized mousedown follows touchend. Over xterm's screen it
  // is harmless (xterm focuses again). Over the strip of the container that
  // xterm's rows do not cover, its default action would blur the textarea we
  // just focused and close the keyboard again, so cancel it there.
  function onMouseDown(event) {
    if (Date.now() - tapFocusedAt > TAP_MOUSEDOWN_WINDOW_MS) return
    tapFocusedAt = -Infinity
    if (!xtermEl.contains(event.target)) event.preventDefault()
  }

  function onTouchCancel() {
    scrolling = false
    tapCandidate = false
  }

  const opts = (passive) => ({ capture: true, passive, signal: controller.signal })
  container.addEventListener('touchstart', onTouchStart, opts(true))
  container.addEventListener('touchmove', onTouchMove, opts(false))
  container.addEventListener('touchend', onTouchEnd, opts(true))
  container.addEventListener('touchcancel', onTouchCancel, opts(true))
  container.addEventListener('mousedown', onMouseDown, opts(false))
}
