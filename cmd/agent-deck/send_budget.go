package main

import (
	"fmt"
	"os"

	"github.com/asheshgoplani/agent-deck/internal/costs"
	"github.com/asheshgoplani/agent-deck/internal/events"
	"github.com/asheshgoplani/agent-deck/internal/session"
)

// deliveryBudgetExceeded is the delivery value of a send or wake refused
// because a cost budget that applies to the target is spent.
const deliveryBudgetExceeded = "budget_exceeded"

// budgetEventKind is the bus frame a refused send or wake publishes.
const budgetEventKind = "session.budget_exceeded"

// budgetForSend is the cost budget gate every send and wake passes before
// anything is typed: `session send` (and so goal nudges, conductor
// heartbeats, bridge and inbox wakes, which all call it) and the queued
// send worker, including `session queue release`. Tests replace it.
var budgetForSend = checkSendBudget

// checkSendBudget evaluates the [costs.budgets] limits that apply to
// sessionID with BudgetChecker.CheckTx. With cost tracking off or no limit
// configured it returns BudgetActionNone without opening the cost store, so
// such setups behave exactly as before. storage and instances may be nil;
// they are then loaded for the profile. A failed budget query never blocks
// a send: it warns and lets the send through.
func checkSendBudget(profile, sessionID string, storage *session.Storage, instances []*session.Instance) costs.BudgetResult {
	none := costs.BudgetResult{Action: costs.BudgetActionNone}
	cfg, err := session.LoadUserConfig()
	if err != nil || cfg == nil || !session.ResolveCostTrackingEnabled(cfg, session.GetEffectiveProfile(profile)) {
		return none
	}
	// An invalid timezone falls back to local time, as in the TUI.
	budgetCfg, _ := costBudgetConfig(cfg)
	if !budgetCfg.Enforced() {
		return none
	}
	if storage == nil {
		storage, instances, _, err = loadSessionData(profile)
		if err != nil {
			fmt.Fprintf(os.Stderr, "Warning: cost budget not checked: %v\n", err)
			return none
		}
		defer func() { _ = storage.Close() }()
	}
	inst := instanceByID(instances, sessionID)
	if inst == nil || storage.GetDB() == nil {
		return none
	}
	var group []string
	for _, other := range instances {
		if other.GroupPath == inst.GroupPath {
			group = append(group, other.ID)
		}
	}
	res, err := costs.NewBudgetChecker(budgetCfg, costs.NewStore(storage.GetDB().DB())).CheckSend(inst.ID, inst.GroupPath, group)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Warning: cost budget not checked: %v\n", err)
		return none
	}
	return res
}

// budgetRefusal is the error text of a refused send or wake.
func budgetRefusal(title string, r costs.BudgetResult) string {
	return fmt.Sprintf("budget exceeded: %s; nothing was sent to '%s'. Raise or remove the limit under [costs.budgets] in config.toml", r.Describe(), title)
}

// publishBudgetExceeded puts the refusal on the events bus so a client
// following session.* learns why a wake did not happen.
func publishBudgetExceeded(profile, sessionID, via string, r costs.BudgetResult) {
	events.PublishProfile(profile, budgetEventKind, sessionID, map[string]any{
		"reason":    r.Reason,
		"used_usd":  float64(r.UsedMicro) / 1e6,
		"limit_usd": float64(r.LimitMicro) / 1e6,
		"via":       via,
	})
}

// enforceSendBudget refuses a `session send` whose target is over budget:
// a clear error (code BUDGET_EXCEEDED, delivery budget_exceeded), a
// session.budget_exceeded event, exit 1. Near a limit (80%) it only notes
// it on stderr.
func enforceSendBudget(profile string, storage *session.Storage, inst *session.Instance, instances []*session.Instance, out *CLIOutput, quiet bool) {
	r := budgetForSend(profile, inst.ID, storage, instances)
	switch r.Action {
	case costs.BudgetActionStop:
		publishBudgetExceeded(profile, inst.ID, "send", r)
		out.ErrorWithData(budgetRefusal(inst.Title, r), ErrCodeBudgetExceeded, map[string]interface{}{
			"delivery":      deliveryBudgetExceeded,
			"session_id":    inst.ID,
			"budget_reason": r.Reason,
			"used_usd":      float64(r.UsedMicro) / 1e6,
			"limit_usd":     float64(r.LimitMicro) / 1e6,
		})
		// exitCLI skips main's deferred bus close; flush the frame first.
		_ = events.CloseDefault()
		exitCLI(1)
	case costs.BudgetActionWarn:
		if !quiet {
			fmt.Fprintf(os.Stderr, "Note: cost budget for '%s': %s\n", inst.Title, r.Describe())
		}
	}
}
