// preactCompat — minimal local polyfill for preact/compat's memo() because
// preact/compat is not vendored in internal/web/static/vendor/. PERF-G uses
// this to wrap SessionRow so unchanged rows bail out of rerenders when the
// parent SessionList re-renders for an unrelated reason (e.g. another
// session's cost update, selection change, or SSE delta).
//
// Implementation uses Preact's Component class directly so Preact's
// reconciler respects shouldComponentUpdate. A plain function-component
// wrapper would NOT short-circuit rerenders because Preact has no bailout
// path for function components outside of this pattern.
import { Component, createElement } from 'preact'

function shallowDiffers(a, b) {
  for (const k in a) {
    if (k !== '__source' && !(k in b)) return true
  }
  for (const k in b) {
    if (k !== '__source' && a[k] !== b[k]) return true
  }
  return false
}

// memo(Comp, areEqual): returns a Preact class component that re-renders
// its child Comp only when areEqual returns false (or shallow prop diff if
// areEqual is omitted). Mirrors the React/preact-compat memo() semantics
// closely enough for SessionRow's PERF-G optimization.
export function memo(Comp, areEqual) {
  class Memoed extends Component {
    shouldComponentUpdate(nextProps) {
      if (areEqual) return !areEqual(this.props, nextProps)
      return shallowDiffers(this.props, nextProps)
    }
    render(props) {
      return createElement(Comp, props)
    }
  }
  Memoed.displayName = 'Memo(' + (Comp.displayName || Comp.name || 'Component') + ')'
  return Memoed
}
