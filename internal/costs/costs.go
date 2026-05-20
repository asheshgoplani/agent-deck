package costs

import (
	"fmt"
	"time"
)

// CostEvent represents a single token usage and cost record.
type CostEvent struct {
	ID               string
	SessionID        string
	Timestamp        time.Time
	Model            string
	InputTokens      int64
	OutputTokens     int64
	CacheReadTokens  int64
	CacheWriteTokens int64
	CostMicrodollars int64 // 1 USD = 1,000,000 microdollars
}

// CostSummary aggregates cost data.
type CostSummary struct {
	TotalCostMicrodollars int64
	TotalInputTokens      int64
	TotalOutputTokens     int64
	TotalCacheReadTokens  int64
	TotalCacheWriteTokens int64
	EventCount            int
}

// SessionCost represents per-session cost totals.
type SessionCost struct {
	SessionID        string
	SessionTitle     string
	Group            string
	CostMicrodollars int64
	EventCount       int
}

// DailyCost represents cost for a single day.
type DailyCost struct {
	Date             time.Time
	CostMicrodollars int64
	Group            string
}

// FormatUSD converts microdollars to a display string.
func FormatUSD(microdollars int64) string {
	return fmt.Sprintf("$%.2f", float64(microdollars)/1_000_000)
}

// RemoteCostSummary mirrors `agent-deck costs summary --json` output. #1101:
// when an SSH remote is configured, the TUI fetches one of these per remote
// and folds the totals into the local cost-line totals so the status bar
// reflects spend across every host.
type RemoteCostSummary struct {
	CostTodayMicrodollars     int64 `json:"cost_today_microdollars"`
	CostYesterdayMicrodollars int64 `json:"cost_yesterday_microdollars"`
	CostThisWeekMicrodollars  int64 `json:"cost_this_week_microdollars"`
	CostLastWeekMicrodollars  int64 `json:"cost_last_week_microdollars"`
	CostThisMonthMicrodollars int64 `json:"cost_this_month_microdollars"`
	CostLastMonthMicrodollars int64 `json:"cost_last_month_microdollars"`
	CostProjectedMicrodollars int64 `json:"cost_projected_microdollars"`
	EventsToday               int   `json:"events_today"`
	EventsThisWeek            int   `json:"events_this_week"`
	EventsThisMonth           int   `json:"events_this_month"`
}

// MergeRemoteCostSummaries sums per-remote summaries into a single aggregate.
// Used by the TUI to display a combined "local + all remotes" cost line.
func MergeRemoteCostSummaries(summaries map[string]*RemoteCostSummary) RemoteCostSummary {
	var out RemoteCostSummary
	for _, s := range summaries {
		if s == nil {
			continue
		}
		out.CostTodayMicrodollars += s.CostTodayMicrodollars
		out.CostYesterdayMicrodollars += s.CostYesterdayMicrodollars
		out.CostThisWeekMicrodollars += s.CostThisWeekMicrodollars
		out.CostLastWeekMicrodollars += s.CostLastWeekMicrodollars
		out.CostThisMonthMicrodollars += s.CostThisMonthMicrodollars
		out.CostLastMonthMicrodollars += s.CostLastMonthMicrodollars
		out.CostProjectedMicrodollars += s.CostProjectedMicrodollars
		out.EventsToday += s.EventsToday
		out.EventsThisWeek += s.EventsThisWeek
		out.EventsThisMonth += s.EventsThisMonth
	}
	return out
}
