package costs

import (
	"encoding/json"
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

type ExportGroup string

const (
	GroupBySession ExportGroup = "session"
	GroupByModel   ExportGroup = "model"
	GroupByDay     ExportGroup = "day"
)

// ExportRow is the stable machine-readable shape used by `costs export`.
// Identity fields which do not apply to model/day grouping remain empty.
type ExportRow struct {
	SessionID        string    `json:"session_id"`
	Title            string    `json:"title"`
	Tool             string    `json:"tool"`
	Model            string    `json:"model"`
	Account          string    `json:"account"`
	Profile          string    `json:"profile"`
	Day              string    `json:"day"`
	Events           int       `json:"events"`
	InputTokens      int64     `json:"input_tokens"`
	OutputTokens     int64     `json:"output_tokens"`
	CacheReadTokens  int64     `json:"cache_read_tokens"`
	CacheWriteTokens int64     `json:"cache_write_tokens"`
	CostUSD          *float64  `json:"cost_usd"`
	FirstTimestamp   time.Time `json:"first_timestamp"`
	LastTimestamp    time.Time `json:"last_timestamp"`
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
	CostTodayKnown            bool  `json:"-"`
	CostYesterdayKnown        bool  `json:"-"`
	CostThisWeekKnown         bool  `json:"-"`
	CostLastWeekKnown         bool  `json:"-"`
	CostThisMonthKnown        bool  `json:"-"`
	CostLastMonthKnown        bool  `json:"-"`
	CostProjectedKnown        bool  `json:"-"`
}

type remoteCostSummaryJSON struct {
	CostTodayMicrodollars     *int64 `json:"cost_today_microdollars"`
	CostYesterdayMicrodollars *int64 `json:"cost_yesterday_microdollars"`
	CostThisWeekMicrodollars  *int64 `json:"cost_this_week_microdollars"`
	CostLastWeekMicrodollars  *int64 `json:"cost_last_week_microdollars"`
	CostThisMonthMicrodollars *int64 `json:"cost_this_month_microdollars"`
	CostLastMonthMicrodollars *int64 `json:"cost_last_month_microdollars"`
	CostProjectedMicrodollars *int64 `json:"cost_projected_microdollars"`
	EventsToday               int    `json:"events_today"`
	EventsThisWeek            int    `json:"events_this_week"`
	EventsThisMonth           int    `json:"events_this_month"`
}

func costPointer(value int64, known bool) *int64 {
	if !known && value == 0 {
		return nil
	}
	return &value
}

func (s RemoteCostSummary) MarshalJSON() ([]byte, error) {
	return json.Marshal(remoteCostSummaryJSON{
		CostTodayMicrodollars: costPointer(s.CostTodayMicrodollars, s.CostTodayKnown), CostYesterdayMicrodollars: costPointer(s.CostYesterdayMicrodollars, s.CostYesterdayKnown),
		CostThisWeekMicrodollars: costPointer(s.CostThisWeekMicrodollars, s.CostThisWeekKnown), CostLastWeekMicrodollars: costPointer(s.CostLastWeekMicrodollars, s.CostLastWeekKnown),
		CostThisMonthMicrodollars: costPointer(s.CostThisMonthMicrodollars, s.CostThisMonthKnown), CostLastMonthMicrodollars: costPointer(s.CostLastMonthMicrodollars, s.CostLastMonthKnown),
		CostProjectedMicrodollars: costPointer(s.CostProjectedMicrodollars, s.CostProjectedKnown), EventsToday: s.EventsToday, EventsThisWeek: s.EventsThisWeek, EventsThisMonth: s.EventsThisMonth,
	})
}

func (s *RemoteCostSummary) UnmarshalJSON(data []byte) error {
	var wire remoteCostSummaryJSON
	if err := json.Unmarshal(data, &wire); err != nil {
		return err
	}
	set := func(dst *int64, known *bool, src *int64) {
		*known = src != nil
		if src != nil {
			*dst = *src
		}
	}
	set(&s.CostTodayMicrodollars, &s.CostTodayKnown, wire.CostTodayMicrodollars)
	set(&s.CostYesterdayMicrodollars, &s.CostYesterdayKnown, wire.CostYesterdayMicrodollars)
	set(&s.CostThisWeekMicrodollars, &s.CostThisWeekKnown, wire.CostThisWeekMicrodollars)
	set(&s.CostLastWeekMicrodollars, &s.CostLastWeekKnown, wire.CostLastWeekMicrodollars)
	set(&s.CostThisMonthMicrodollars, &s.CostThisMonthKnown, wire.CostThisMonthMicrodollars)
	set(&s.CostLastMonthMicrodollars, &s.CostLastMonthKnown, wire.CostLastMonthMicrodollars)
	set(&s.CostProjectedMicrodollars, &s.CostProjectedKnown, wire.CostProjectedMicrodollars)
	s.EventsToday, s.EventsThisWeek, s.EventsThisMonth = wire.EventsToday, wire.EventsThisWeek, wire.EventsThisMonth
	return nil
}

// MergeRemoteCostSummaries sums per-remote summaries into a single aggregate.
// Used by the TUI to display a combined "local + all remotes" cost line.
func MergeRemoteCostSummaries(summaries map[string]*RemoteCostSummary) RemoteCostSummary {
	var out RemoteCostSummary
	first := true
	for _, s := range summaries {
		if s == nil {
			continue
		}
		known := func(flag bool, value int64) bool { return flag || value != 0 }
		if first {
			out.CostTodayKnown, out.CostYesterdayKnown, out.CostThisWeekKnown = true, true, true
			out.CostLastWeekKnown, out.CostThisMonthKnown, out.CostLastMonthKnown, out.CostProjectedKnown = true, true, true, true
			first = false
		}
		out.CostTodayKnown = out.CostTodayKnown && known(s.CostTodayKnown, s.CostTodayMicrodollars)
		out.CostYesterdayKnown = out.CostYesterdayKnown && known(s.CostYesterdayKnown, s.CostYesterdayMicrodollars)
		out.CostThisWeekKnown = out.CostThisWeekKnown && known(s.CostThisWeekKnown, s.CostThisWeekMicrodollars)
		out.CostLastWeekKnown = out.CostLastWeekKnown && known(s.CostLastWeekKnown, s.CostLastWeekMicrodollars)
		out.CostThisMonthKnown = out.CostThisMonthKnown && known(s.CostThisMonthKnown, s.CostThisMonthMicrodollars)
		out.CostLastMonthKnown = out.CostLastMonthKnown && known(s.CostLastMonthKnown, s.CostLastMonthMicrodollars)
		out.CostProjectedKnown = out.CostProjectedKnown && known(s.CostProjectedKnown, s.CostProjectedMicrodollars)
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
