// Relative-time formatting for the web UI.
//
// This is a byte-for-byte mirror of the Go formatter in
// internal/ui/humanize_since.go. The two are pinned to the same canonical
// parity table (Go: internal/ui/humanize_since_test.go, JS:
// tests/web/unit/timeFmt.test.js) so the TUI and web never diverge.
// Compact, two-component, floor math, month≈30d / year≈365d.

const MIN = 60 * 1000
const HOUR = 60 * MIN
const DAY = 24 * HOUR
const WEEK = 7 * DAY
const MONTH = 30 * DAY
const YEAR = 365 * DAY

// Go time.Time zero (0001-01-01T00:00:00Z) as epoch ms. `lastAccessedAt`
// serializes to this when unset, and must read as "unknown", not a huge age.
const GO_ZERO_MS = -62135596800000

function twoUnitAgo(d, primary, secondary, pu, su) {
  const p = Math.floor(d / primary)
  const s = Math.floor((d - p * primary) / secondary)
  if (s === 0) return `${p}${pu} ago`
  return `${p}${pu} ${s}${su} ago`
}

// humanizeSince maps an elapsed (past) duration in ms to a compact relative
// string. Mirror of Go humanizeSince.
export function humanizeSince(ms) {
  const d = ms
  if (d < MIN) return 'just now' // includes negative (future/clock skew)
  if (d < HOUR) return `${Math.floor(d / MIN)}m ago`
  if (d < DAY) return twoUnitAgo(d, HOUR, MIN, 'h', 'm')
  if (d < WEEK) return twoUnitAgo(d, DAY, HOUR, 'd', 'h')
  if (d < MONTH) return twoUnitAgo(d, WEEK, DAY, 'w', 'd')
  if (d < YEAR) return twoUnitAgo(d, MONTH, WEEK, 'mo', 'w')
  return twoUnitAgo(d, YEAR, MONTH, 'y', 'mo')
}

// formatRelativeTime formats an ISO timestamp (or ms epoch) relative to `now`.
// Absent / unparseable / Go-zero timestamps render as an em dash.
export function formatRelativeTime(iso, now = Date.now()) {
  if (iso === null || iso === undefined || iso === '') return '—'
  const t = typeof iso === 'number' ? iso : new Date(iso).getTime()
  if (!Number.isFinite(t)) return '—'
  if (t <= GO_ZERO_MS) return '—'
  return humanizeSince(now - t)
}
