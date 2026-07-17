package ui

import (
	"fmt"
	"time"
)

// humanizeSince renders an elapsed (past) duration as a compact, two-component
// relative string, e.g. "just now", "45m ago", "3h 20m ago", "2d 5h ago",
// "5mo 1w ago", "2y 3mo ago". It is the single source of truth for relative
// timestamps in the TUI; the web mirrors it byte-for-byte in
// internal/web/static/app/timeFmt.js. Both are pinned to the same parity table
// in internal/ui/humanize_since_test.go and tests/web/unit/timeFmt.test.js.
//
// Design: two components max (primary + next-smaller), secondary dropped when
// zero, floor (truncate) math, month≈30d / year≈365d. Callers own the
// zero-time / running / archived sentinels — this maps a duration only.
func humanizeSince(d time.Duration) string {
	const (
		minute = time.Minute
		hour   = time.Hour
		day    = 24 * hour
		week   = 7 * day
		month  = 30 * day
		year   = 365 * day
	)
	switch {
	case d < minute: // includes negative (future/clock skew)
		return "just now"
	case d < hour:
		return fmt.Sprintf("%dm ago", int(d/minute))
	case d < day:
		return twoUnitAgo(d, hour, minute, "h", "m")
	case d < week:
		return twoUnitAgo(d, day, hour, "d", "h")
	case d < month:
		return twoUnitAgo(d, week, day, "w", "d")
	case d < year:
		return twoUnitAgo(d, month, week, "mo", "w")
	default:
		return twoUnitAgo(d, year, month, "y", "mo")
	}
}

// twoUnitAgo formats d as "<p><pu> <s><su> ago", dropping the secondary term
// when it floors to zero ("<p><pu> ago").
func twoUnitAgo(d, primary, secondary time.Duration, pu, su string) string {
	p := d / primary
	s := (d - p*primary) / secondary
	if s == 0 {
		return fmt.Sprintf("%d%s ago", p, pu)
	}
	return fmt.Sprintf("%d%s %d%s ago", p, pu, s, su)
}
