package session

import (
	"fmt"
	"math"
	"os"
	"time"

	"github.com/asheshgoplani/agent-deck/internal/costs"
	"github.com/asheshgoplani/agent-deck/internal/events"
)

// budgetEventKind is the bus frame a refused send or wake publishes.
const budgetEventKind = "session.budget_exceeded"

// WakeBudget evaluates the [costs.budgets] limits that apply to target
// with BudgetChecker.CheckTx. Unless cost tracking is on and the budgets
// have enforce = true with a limit set, it returns BudgetActionNone without
// opening the cost store, so budgets stay display-only. A failed budget
// query never blocks a send: it warns and lets the send through. instances
// are the profile's sessions as the caller loaded them (for group
// membership). storage may be nil; the profile's storage is then opened for
// the cost store only, and a storage the caller passes is left open.
func WakeBudget(profile string, target *Instance, instances []*Instance, storage *Storage) costs.BudgetResult {
	none := costs.BudgetResult{Action: costs.BudgetActionNone}
	if target == nil {
		return none
	}
	cfg, err := LoadUserConfig()
	if err != nil || cfg == nil || !ResolveCostTrackingEnabled(cfg, GetEffectiveProfile(profile)) {
		return none
	}
	// An invalid timezone falls back to local time, as in the TUI.
	budgetCfg, _ := CostBudgetConfig(cfg)
	if !budgetCfg.Enforced() {
		return none
	}
	if storage == nil {
		storage, err = NewStorageWithProfile(profile)
		if err != nil {
			fmt.Fprintf(os.Stderr, "Warning: cost budget not checked: %v\n", err)
			return none
		}
		defer func() { _ = storage.Close() }()
	}
	if storage.GetDB() == nil {
		return none
	}
	res, err := costs.NewBudgetChecker(budgetCfg, costs.NewStore(storage.GetDB().DB())).
		CheckSend(target.ID, target.GroupPath, budgetGroupMembers(target, instances))
	if err != nil {
		fmt.Fprintf(os.Stderr, "Warning: cost budget not checked: %v\n", err)
		return none
	}
	return res
}

// budgetGroupMembers is the sessions a group daily limit sums: every session
// whose group path equals the target's exactly (a subgroup is its own group),
// the target included.
func budgetGroupMembers(target *Instance, instances []*Instance) []string {
	ids := []string{target.ID}
	for _, other := range instances {
		if other != nil && other.ID != target.ID && other.GroupPath == target.GroupPath {
			ids = append(ids, other.ID)
		}
	}
	return ids
}

// CostBudgetConfig keeps the dashboard and enforcement on the same settings.
func CostBudgetConfig(cfg *UserConfig) (costs.BudgetConfig, error) {
	result := costs.BudgetConfig{}
	if cfg == nil {
		return result, nil
	}
	settings := cfg.Costs.Budgets
	result.Enforce = settings.Enforce
	result.DailyLimit = int64(math.Round(settings.DailyLimit * 1_000_000))
	result.WeeklyLimit = int64(math.Round(settings.WeeklyLimit * 1_000_000))
	result.MonthlyLimit = int64(math.Round(settings.MonthlyLimit * 1_000_000))
	result.GroupLimits = make(map[string]int64, len(settings.Groups))
	for name, budget := range settings.Groups {
		result.GroupLimits[name] = int64(math.Round(budget.DailyLimit * 1_000_000))
	}
	result.SessionLimits = make(map[string]int64, len(settings.Sessions))
	for id, budget := range settings.Sessions {
		result.SessionLimits[id] = int64(math.Round(budget.TotalLimit * 1_000_000))
	}
	var err error
	result.Timezone, err = time.LoadLocation(cfg.Costs.GetTimezone())
	if err != nil {
		return result, fmt.Errorf("costs timezone: %w", err)
	}
	return result, nil
}

// PublishBudgetExceeded puts the refusal on the events bus so a client
// following session.* learns why a wake did not happen.
func PublishBudgetExceeded(profile, sessionID, via string, r costs.BudgetResult) {
	events.PublishProfile(profile, budgetEventKind, sessionID, map[string]any{
		"reason":    r.Reason,
		"used_usd":  r.UsedUSD(),
		"limit_usd": r.LimitUSD(),
		"via":       via,
	})
}
