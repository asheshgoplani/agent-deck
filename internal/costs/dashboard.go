package costs

import (
	"fmt"
	"sort"
	"time"
)

// DashboardDailyEntry is shared by the web Costs view and the CLI.
type DashboardDailyEntry struct {
	Date    string  `json:"date"`
	CostUSD float64 `json:"cost_usd"`
}

func microToUSD(micro int64) float64 { return float64(micro) / 1_000_000 }

func (s *Store) DashboardDaily(days int) ([]DashboardDailyEntry, error) {
	if days < 1 || days > 365 {
		return nil, fmt.Errorf("days must be between 1 and 365")
	}
	now := s.now().UTC()
	daily, err := s.TotalByDateRange(now.AddDate(0, 0, -days).Truncate(24*time.Hour), now.AddDate(0, 0, 1).Truncate(24*time.Hour))
	if err != nil {
		return nil, err
	}
	result := make([]DashboardDailyEntry, 0, len(daily))
	for _, dc := range daily {
		result = append(result, DashboardDailyEntry{Date: dc.Date.Format("2006-01-02"), CostUSD: microToUSD(dc.CostMicrodollars)})
	}
	return result, nil
}

type DashboardSessionEntry struct {
	SessionID    string  `json:"session_id"`
	Title        string  `json:"title"`
	Group        string  `json:"group"`
	CostUSD      float64 `json:"cost_usd"`
	Events       int     `json:"events"`
	InputTokens  int64   `json:"input_tokens"`
	OutputTokens int64   `json:"output_tokens"`
	CacheRead    int64   `json:"cache_read"`
	CacheWrite   int64   `json:"cache_write"`
	Model        string  `json:"model"`
}

// ValidateCostPeriod validates a dashboard period before opening storage.
func ValidateCostPeriod(period string) error {
	_, err := costPeriodWhere(period)
	return err
}

// periodClause is a WHERE fragment for cost_events. Only costPeriodWhere
// (a fixed set of constant fragments) and the empty literal produce one, so
// any query text built from it carries no caller input; values stay bound
// through ? placeholders.
type periodClause string

// costPeriodWhere is also used by summary so the period picker cannot drift.
func costPeriodWhere(period string) (periodClause, error) {
	switch period {
	case "all":
		return "", nil
	case "today":
		return `WHERE timestamp >= date('now', 'start of day')`, nil
	// Stored timestamps are RFC3339; normalize before comparing with SQLite datetime.
	case "7d":
		return `WHERE datetime(timestamp) >= datetime('now', '-7 days')`, nil
	case "30d":
		return `WHERE datetime(timestamp) >= datetime('now', '-30 days')`, nil
	case "week":
		return `WHERE timestamp >= date('now', 'weekday 1', '-7 days')`, nil
	case "month":
		return `WHERE timestamp >= date('now', 'start of month')`, nil
	default:
		return "", fmt.Errorf("period must be today, week, month, or all")
	}
}

func (s *Store) DashboardSessions(limit int, period string) ([]DashboardSessionEntry, error) {
	if limit < 1 || limit > 500 {
		return nil, fmt.Errorf("limit must be between 1 and 500")
	}
	where, err := costPeriodWhere(period)
	if err != nil {
		return nil, err
	}
	// Aggregate each model once; ranking and token totals use the same window.
	//nolint:gosec // G202: where is a periodClause, a constant fragment from costPeriodWhere; limit is bound via ?
	rows, err := s.db.Query(`WITH model_totals AS (
 SELECT session_id, model, SUM(cost_microdollars) AS cost, COUNT(*) AS events,
 SUM(input_tokens) AS input_tokens, SUM(output_tokens) AS output_tokens,
 SUM(cache_read_tokens) AS cache_read, SUM(cache_write_tokens) AS cache_write
 FROM cost_events `+string(where)+` GROUP BY session_id,model
 ), ranked AS (
 SELECT *, ROW_NUMBER() OVER (PARTITION BY session_id ORDER BY cost DESC,model) AS rank
 FROM model_totals
 )
 SELECT r.session_id,COALESCE(i.title,r.session_id),COALESCE(i.group_path,''),
 SUM(r.cost),SUM(r.events),SUM(r.input_tokens),SUM(r.output_tokens),SUM(r.cache_read),SUM(r.cache_write),
 MAX(CASE WHEN r.rank=1 THEN r.model END)
 FROM ranked r LEFT JOIN instances i ON r.session_id=i.id
 GROUP BY r.session_id ORDER BY SUM(r.cost) DESC,r.session_id LIMIT ?`, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := make([]DashboardSessionEntry, 0)
	for rows.Next() {
		var entry DashboardSessionEntry
		var micro int64
		if err := rows.Scan(&entry.SessionID, &entry.Title, &entry.Group, &micro, &entry.Events, &entry.InputTokens, &entry.OutputTokens, &entry.CacheRead, &entry.CacheWrite, &entry.Model); err != nil {
			return nil, err
		}
		entry.CostUSD = microToUSD(micro)
		result = append(result, entry)
	}
	return result, rows.Err()
}

func (s *Store) DashboardModels(period string) (map[string]float64, error) {
	where, err := costPeriodWhere(period)
	if err != nil {
		return nil, err
	}
	models, err := s.costByModel(where)
	if err != nil {
		return nil, err
	}
	result := make(map[string]float64, len(models))
	for model, micro := range models {
		result[model] = microToUSD(micro)
	}
	return result, nil
}

type DashboardGroupEntry struct {
	Group    string  `json:"group"`
	CostUSD  float64 `json:"cost_usd"`
	Events   int     `json:"events"`
	Sessions int     `json:"sessions"`
}

func (s *Store) DashboardGroups(period string) ([]DashboardGroupEntry, error) {
	where, err := costPeriodWhere(period)
	if err != nil {
		return nil, err
	}
	sessions, err := s.topSessionsByCost(1000, where)
	if err != nil {
		return nil, err
	}
	groups := make(map[string]*DashboardGroupEntry)
	for _, sc := range sessions {
		name := sc.Group
		if name == "" {
			name = "(ungrouped)"
		}
		entry := groups[name]
		if entry == nil {
			entry = &DashboardGroupEntry{Group: name}
			groups[name] = entry
		}
		entry.CostUSD += microToUSD(sc.CostMicrodollars)
		entry.Events += sc.EventCount
		entry.Sessions++
	}
	result := make([]DashboardGroupEntry, 0, len(groups))
	for _, entry := range groups {
		result = append(result, *entry)
	}
	sort.Slice(result, func(i, j int) bool { return result[i].Group < result[j].Group })
	return result, nil
}
