// viewportInsets.js -- producer for the two layout variables styles.src.css
// already consumes on `.app`:
//
//     height: var(--app-height);
//     padding-bottom: calc(var(--keyboard-inset) + env(safe-area-inset-bottom));
//
// Both were declared in :root and never written, so `--app-height` stayed at its
// `100vh` default and `--keyboard-inset` at `0px`. On iOS Safari `100vh` is the
// *large* viewport -- the height the page would have with the toolbars hidden --
// so while they are showing, an element sized to it runs off the bottom of the
// screen and takes the mobile tab bar with it. `window.innerHeight` tracks the
// toolbars, which is why it, and not `100vh`, is what `--app-height` should be.
//
// The software keyboard is a separate axis: it never changes the layout
// viewport, only the visual one. `visualViewport.height` is what shrinks, and
// `offsetTop` is how far iOS scrolled the visual viewport up to keep the focused
// field in sight -- that shift is not keyboard, so it comes off the overlap:
//
//     inset = innerHeight - visualViewport.height - visualViewport.offsetTop
//
// clamped at zero because iOS reports a visual viewport taller than the layout
// one during rubber-band overscroll.
//
// Measured on iPhone (iOS 18.7, Safari 26.5): innerHeight 660 throughout;
// visualViewport.height 660 with the keyboard closed and 376 with it open, so
// the inset is 284. `100dvh` also reads 660 in both states, which is why it can
// only serve as the static fallback and cannot carry the keyboard on its own.

export function applyViewportInsets(win, root) {
  const layout = win.innerHeight

  // A background tab, a bfcache restore, and the moment before first layout all
  // report 0. Publishing it would flatten `.app` to zero height; holding the last
  // good value leaves the CSS `100dvh` fallback in charge until a real
  // measurement arrives.
  if (!layout) return

  const visual = win.visualViewport
  const inset = visual ? Math.max(0, layout - visual.height - visual.offsetTop) : 0

  root.style.setProperty('--app-height', `${layout}px`)
  root.style.setProperty('--keyboard-inset', `${inset}px`)
}

// Publishes now and on every event that can move either number. Listeners are
// registered with the caller's AbortSignal, the teardown convention the rest of
// the web UI uses (one controller per mount, aborted on unmount).
export function installViewportInsets(win, root, signal) {
  const publish = () => applyViewportInsets(win, root)
  const opts = { signal }

  publish()

  win.addEventListener('resize', publish, opts)
  win.addEventListener('orientationchange', publish, opts)

  const visual = win.visualViewport
  if (visual) {
    visual.addEventListener('resize', publish, opts)
    visual.addEventListener('scroll', publish, opts)
  }
}
