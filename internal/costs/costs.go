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
