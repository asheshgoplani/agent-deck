package session

import "time"

// TimeFilterMode narrows the session list to sessions whose last activity
// falls within a recency window. It is toggled from the TUI (cycled by a
// hotkey) and persisted across restarts, the same way GroupViewMode is (see
// group_view_mode.go).
type TimeFilterMode int

const (
	// TimeFilterAll shows every session regardless of activity time. This is
	// the default (zero value), matching the existing statusFilter="" and
	// GroupViewNormal=0 convention: an unset filter must be the no-op state.
	TimeFilterAll TimeFilterMode = iota
	// TimeFilterToday shows sessions active since local midnight.
	TimeFilterToday
	// TimeFilter3Days shows sessions active within the last 3*24h.
	TimeFilter3Days
	// TimeFilter7Days shows sessions active within the last 7*24h.
	TimeFilter7Days
)

// TimeFilterModeCount is the number of cycle-able modes (used for "(mode+1)%N").
const TimeFilterModeCount = 4

// Label returns a short human-readable name for the mode (for status hints).
func (m TimeFilterMode) Label() string {
	switch m {
	case TimeFilterToday:
		return "Today"
	case TimeFilter3Days:
		return "Last 3 days"
	case TimeFilter7Days:
		return "Last 7 days"
	default:
		return "All time"
	}
}

// Matches reports whether t (a session's last-activity time, typically
// Instance.DisplayLastActivityTime()) falls within this mode's window,
// evaluated relative to now.
//
// TimeFilterToday uses a local-calendar-day boundary (since local midnight)
// rather than "within the last 24h": that is how a human reads the word
// "today" regardless of what time it currently is. The multi-day modes use a
// simple rolling N*24h window instead of a calendar-day count — the
// day-boundary fuzziness that matters for "today" is negligible at 3-7 days,
// and a rolling window avoids re-deriving "N calendar days ago" per timezone.
func (m TimeFilterMode) Matches(t, now time.Time) bool {
	switch m {
	case TimeFilterToday:
		y, mo, d := now.Date()
		startOfToday := time.Date(y, mo, d, 0, 0, 0, 0, now.Location())
		return !t.Before(startOfToday)
	case TimeFilter3Days:
		return !t.Before(now.Add(-3 * 24 * time.Hour))
	case TimeFilter7Days:
		return !t.Before(now.Add(-7 * 24 * time.Hour))
	default:
		return true
	}
}
