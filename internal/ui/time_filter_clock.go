package ui

import (
	"time"

	"github.com/asheshgoplani/agent-deck/internal/session"
)

// recordTimeFilterExpiry runs only for known timestamps that passed the same
// eligibility and recency checks as row filtering. Collapsed groups participate.
func (h *Home) recordTimeFilterExpiry(activity, now time.Time) {
	var expiry time.Time
	switch h.timeFilter {
	case session.TimeFilterToday:
		local := activity.In(now.Location())
		// Calendar arithmetic handles 23/25-hour days. A future activity timestamp
		// stays eligible until its own local day ends, matching TimeFilterMode.Matches.
		expiry = time.Date(local.Year(), local.Month(), local.Day()+1, 0, 0, 0, 0, now.Location())
	case session.TimeFilter3Days:
		expiry = activity.Add(3*24*time.Hour + time.Nanosecond)
	case session.TimeFilter7Days:
		expiry = activity.Add(7*24*time.Hour + time.Nanosecond)
	default:
		return
	}
	// Rolling Matches includes the cutoff itself, so expiry is one ns later.
	if h.nextTimeFilterExpiry.IsZero() || expiry.Before(h.nextTimeFilterExpiry) {
		h.nextTimeFilterExpiry = expiry
	}
}
