package costs

import (
	"database/sql"
	"time"
)

type BudgetAction int

const (
	BudgetActionNone BudgetAction = iota
	BudgetActionWarn
	BudgetActionStop
)

type BudgetResult struct {
	Action     BudgetAction
	Reason     string
	UsedMicro  int64
	LimitMicro int64
	Percentage float64
}

// BudgetConfig holds budget limits in microdollars.
type BudgetConfig struct {
	DailyLimit    int64
	WeeklyLimit   int64
	MonthlyLimit  int64
	GroupLimits   map[string]int64 // group name -> daily limit in microdollars
	SessionLimits map[string]int64 // session ID -> total lifetime limit in microdollars
	Timezone      *time.Location   // for determining day/week/month boundaries
}

type BudgetChecker struct {
	cfg   BudgetConfig
	store *Store
}

func NewBudgetChecker(cfg BudgetConfig, store *Store) *BudgetChecker {
	return &BudgetChecker{cfg: cfg, store: store}
}

// CheckTx evaluates all budget limits within a transaction.
// This must be called within the same transaction as the cost event INSERT.
func (b *BudgetChecker) CheckTx(tx *sql.Tx, sessionID, groupName string, groupSessionIDs []string) (BudgetResult, error) {
	worst := BudgetResult{Action: BudgetActionNone}
	tz := b.cfg.Timezone
	if tz == nil {
		tz = time.Local
	}

	// Session lifetime limit
	if limit, ok := b.cfg.SessionLimits[sessionID]; ok && limit > 0 {
		total, err := b.store.RunningTotal(tx, sessionID, time.Time{}) // all time
		if err != nil {
			return BudgetResult{Action: BudgetActionStop, Reason: "budget query failed"}, err
		}
		r := evaluate(total, limit, "session lifetime limit exceeded")
		if r.Action > worst.Action {
			worst = r
		}
	}

	// Daily global limit
	if b.cfg.DailyLimit > 0 {
		total, err := b.store.GlobalRunningTotal(tx, startOfDay(tz))
		if err != nil {
			return BudgetResult{Action: BudgetActionStop, Reason: "budget query failed"}, err
		}
		r := evaluate(total, b.cfg.DailyLimit, "daily global limit exceeded")
		if r.Action > worst.Action {
			worst = r
		}
	}

	// Weekly global limit
	if b.cfg.WeeklyLimit > 0 {
		total, err := b.store.GlobalRunningTotal(tx, startOfWeek(tz))
		if err != nil {
			return BudgetResult{Action: BudgetActionStop, Reason: "budget query failed"}, err
		}
		r := evaluate(total, b.cfg.WeeklyLimit, "weekly global limit exceeded")
		if r.Action > worst.Action {
			worst = r
		}
	}

	// Monthly global limit
	if b.cfg.MonthlyLimit > 0 {
		total, err := b.store.GlobalRunningTotal(tx, startOfMonth(tz))
		if err != nil {
			return BudgetResult{Action: BudgetActionStop, Reason: "budget query failed"}, err
		}
		r := evaluate(total, b.cfg.MonthlyLimit, "monthly global limit exceeded")
		if r.Action > worst.Action {
			worst = r
		}
	}

	// Group daily limit
	if limit, ok := b.cfg.GroupLimits[groupName]; ok && limit > 0 && len(groupSessionIDs) > 0 {
		total, err := b.store.GroupRunningTotal(tx, groupSessionIDs, startOfDay(tz))
		if err != nil {
			return BudgetResult{Action: BudgetActionStop, Reason: "budget query failed"}, err
		}
		r := evaluate(total, limit, "group daily limit exceeded")
		if r.Action > worst.Action {
			worst = r
		}
	}

	return worst, nil
}

// Check is a convenience for non-transactional checks (e.g., TUI display).
func (b *BudgetChecker) Check(sessionID, groupName string) BudgetResult {
	worst := BudgetResult{Action: BudgetActionNone}

	if b.cfg.DailyLimit > 0 {
		summary, _ := b.store.TotalToday()
		r := evaluate(summary.TotalCostMicrodollars, b.cfg.DailyLimit, "daily global limit exceeded")
		if r.Action > worst.Action {
			worst = r
		}
	}

	if b.cfg.WeeklyLimit > 0 {
		summary, _ := b.store.TotalThisWeek()
		r := evaluate(summary.TotalCostMicrodollars, b.cfg.WeeklyLimit, "weekly global limit exceeded")
		if r.Action > worst.Action {
			worst = r
		}
	}

	if b.cfg.MonthlyLimit > 0 {
		summary, _ := b.store.TotalThisMonth()
		r := evaluate(summary.TotalCostMicrodollars, b.cfg.MonthlyLimit, "monthly global limit exceeded")
		if r.Action > worst.Action {
			worst = r
		}
	}

	return worst
}

func evaluate(used, limit int64, reason string) BudgetResult {
	if limit <= 0 {
		return BudgetResult{Action: BudgetActionNone}
	}
	pct := float64(used) / float64(limit)
	if pct >= 1.0 {
		return BudgetResult{Action: BudgetActionStop, Reason: reason, UsedMicro: used, LimitMicro: limit, Percentage: pct * 100}
	}
	if pct >= 0.8 {
		return BudgetResult{Action: BudgetActionWarn, Reason: reason, UsedMicro: used, LimitMicro: limit, Percentage: pct * 100}
	}
	return BudgetResult{Action: BudgetActionNone, Percentage: pct * 100}
}

func startOfDay(tz *time.Location) time.Time {
	now := time.Now().In(tz)
	return time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, tz)
}

func startOfWeek(tz *time.Location) time.Time {
	now := time.Now().In(tz)
	weekday := int(now.Weekday())
	if weekday == 0 {
		weekday = 7 // Sunday = 7
	}
	monday := now.AddDate(0, 0, -(weekday - 1))
	return time.Date(monday.Year(), monday.Month(), monday.Day(), 0, 0, 0, 0, tz)
}

func startOfMonth(tz *time.Location) time.Time {
	now := time.Now().In(tz)
	return time.Date(now.Year(), now.Month(), 1, 0, 0, 0, 0, tz)
}
