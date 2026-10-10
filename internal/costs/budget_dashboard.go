package costs

import (
	"fmt"
	"time"
)

// BudgetUsage reports spend and a configured limit in dollars.
type BudgetUsage struct {
	UsedUSD  float64 `json:"used_usd"`
	LimitUSD float64 `json:"limit_usd"`
}

// BudgetDashboard includes only positive, configured budget limits.
type BudgetDashboard struct {
	Daily    *BudgetUsage           `json:"daily,omitempty"`
	Weekly   *BudgetUsage           `json:"weekly,omitempty"`
	Monthly  *BudgetUsage           `json:"monthly,omitempty"`
	Groups   map[string]BudgetUsage `json:"groups,omitempty"`
	Sessions map[string]BudgetUsage `json:"sessions,omitempty"`
}

// Dashboard reads budget usage in a single transaction using the same windows
// and sums as CheckTx. Group membership uses exact current instance group paths;
// session budgets cover lifetime spend, including sessions no longer present.
func (b *BudgetChecker) Dashboard() (BudgetDashboard, error) {
	report := BudgetDashboard{}
	tx, err := b.store.DB().Begin()
	if err != nil {
		return report, err
	}
	defer func() { _ = tx.Rollback() }()
	tz := b.cfg.Timezone
	if tz == nil {
		tz = time.Local
	}
	usage := func(used, limit int64) BudgetUsage {
		return BudgetUsage{UsedUSD: microToUSD(used), LimitUSD: microToUSD(limit)}
	}
	for _, period := range []struct {
		name   string
		limit  int64
		since  time.Time
		target **BudgetUsage
	}{
		{"daily", b.cfg.DailyLimit, startOfDay(tz), &report.Daily},
		{"weekly", b.cfg.WeeklyLimit, startOfWeek(tz), &report.Weekly},
		{"monthly", b.cfg.MonthlyLimit, startOfMonth(tz), &report.Monthly},
	} {
		if period.limit <= 0 {
			continue
		}
		total, err := b.store.GlobalRunningTotal(tx, period.since)
		if err != nil {
			return BudgetDashboard{}, fmt.Errorf("%s budget usage: %w", period.name, err)
		}
		value := usage(total, period.limit)
		*period.target = &value
	}
	for group, limit := range b.cfg.GroupLimits {
		if limit <= 0 {
			continue
		}
		rows, err := tx.Query(`SELECT id FROM instances WHERE group_path = ?`, group)
		if err != nil {
			return BudgetDashboard{}, fmt.Errorf("group budget membership: %w", err)
		}
		var ids []string
		for rows.Next() {
			var id string
			if err := rows.Scan(&id); err != nil {
				_ = rows.Close()
				return BudgetDashboard{}, err
			}
			ids = append(ids, id)
		}
		err = rows.Err()
		_ = rows.Close()
		if err != nil {
			return BudgetDashboard{}, err
		}
		total, err := b.store.GroupRunningTotal(tx, ids, startOfDay(tz))
		if err != nil {
			return BudgetDashboard{}, fmt.Errorf("group budget usage: %w", err)
		}
		if report.Groups == nil {
			report.Groups = make(map[string]BudgetUsage)
		}
		report.Groups[group] = usage(total, limit)
	}
	for id, limit := range b.cfg.SessionLimits {
		if limit <= 0 {
			continue
		}
		total, err := b.store.RunningTotal(tx, id, time.Time{})
		if err != nil {
			return BudgetDashboard{}, fmt.Errorf("session budget usage: %w", err)
		}
		if report.Sessions == nil {
			report.Sessions = make(map[string]BudgetUsage)
		}
		report.Sessions[id] = usage(total, limit)
	}
	if err := tx.Commit(); err != nil {
		return BudgetDashboard{}, err
	}
	return report, nil
}
