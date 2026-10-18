package main

import (
	"fmt"
	"os"
	"time"

	"github.com/asheshgoplani/agent-deck/internal/costs"
	"github.com/asheshgoplani/agent-deck/internal/events"
	"github.com/asheshgoplani/agent-deck/internal/session"
)

// deliveryBudgetExceeded is the delivery value of a send or wake refused
// because a cost budget that applies to the target is spent.
const deliveryBudgetExceeded = "budget_exceeded"

// budgetEventKind is the bus frame a refused send or wake publishes.
const budgetEventKind = "session.budget_exceeded"

// ignoreBudgetFlagHelp is the help of every --ignore-budget flag.
const ignoreBudgetFlagHelp = "Send even when an enforced [costs.budgets] limit is spent (for messages a person starts)"

// budgetForSend is the cost budget gate for agent-initiated sends and
// wakes: `session send` (and so goal nudges, conductor heartbeats and inbox
// wakes), the queued send worker and `session queue release`, `launch -m`,
// `session start -m` and the MCP restart's "continue". target is the
// session the text goes to, instances the profile's sessions as the caller
// loaded them (for group membership). storage may be nil; the profile's
// storage is then opened for the cost store only. Tests replace it.
var budgetForSend = checkSendBudget

// checkSendBudget evaluates the [costs.budgets] limits that apply to target
// with BudgetChecker.CheckTx. Unless cost tracking is on and the budgets
// have enforce = true with a limit set, it returns BudgetActionNone without
// opening the cost store, so budgets stay display-only. A failed budget
// query never blocks a send: it warns and lets the send through.
func checkSendBudget(profile string, target *session.Instance, instances []*session.Instance, storage *session.Storage) costs.BudgetResult {
	none := costs.BudgetResult{Action: costs.BudgetActionNone}
	if target == nil {
		return none
	}
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
		storage, err = session.NewStorageWithProfile(profile)
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
func budgetGroupMembers(target *session.Instance, instances []*session.Instance) []string {
	ids := []string{target.ID}
	for _, other := range instances {
		if other != nil && other.ID != target.ID && other.GroupPath == target.GroupPath {
			ids = append(ids, other.ID)
		}
	}
	return ids
}

// budgetRefusal is the error text of a refused send or wake.
func budgetRefusal(title string, r costs.BudgetResult) string {
	return fmt.Sprintf("budget exceeded: %s; nothing was sent to '%s'. Raise or remove the limit under [costs.budgets] in config.toml, or pass --ignore-budget", r.Describe(), title)
}

// budgetRefusalData is the --json detail of a refusal. resets_at is when
// the limit's window starts over; it is absent for a session lifetime
// limit, which does not reset.
func budgetRefusalData(sessionID string, r costs.BudgetResult) map[string]interface{} {
	data := map[string]interface{}{
		"delivery":      deliveryBudgetExceeded,
		"session_id":    sessionID,
		"budget_reason": r.Reason,
		"used_usd":      r.UsedUSD(),
		"limit_usd":     r.LimitUSD(),
	}
	if !r.ResetsAt.IsZero() {
		data["resets_at"] = r.ResetsAt.Format(time.RFC3339)
	}
	return data
}

// publishBudgetExceeded puts the refusal on the events bus so a client
// following session.* learns why a wake did not happen.
func publishBudgetExceeded(profile, sessionID, via string, r costs.BudgetResult) {
	events.PublishProfile(profile, budgetEventKind, sessionID, map[string]any{
		"reason":    r.Reason,
		"used_usd":  r.UsedUSD(),
		"limit_usd": r.LimitUSD(),
		"via":       via,
	})
}

// enforceSendBudget refuses a send or wake whose target is over an enforced
// budget: a clear error (code BUDGET_EXCEEDED, delivery budget_exceeded), a
// session.budget_exceeded event, exit 1. Near a limit (80%) it only notes it
// on stderr. via names the path in the event ("send", "launch", "start").
func enforceSendBudget(profile, via string, storage *session.Storage, inst *session.Instance, instances []*session.Instance, out *CLIOutput, quiet bool, beforeExit func()) {
	r := budgetForSend(profile, inst, instances, storage)
	switch r.Action {
	case costs.BudgetActionStop:
		if beforeExit != nil {
			beforeExit()
		}
		publishBudgetExceeded(profile, inst.ID, via, r)
		out.ErrorWithData(budgetRefusal(inst.Title, r), ErrCodeBudgetExceeded, budgetRefusalData(inst.ID, r))
		// exitCLI skips main's deferred bus close; flush the frame first.
		_ = events.CloseDefault()
		exitCLI(1)
	case costs.BudgetActionWarn:
		if !quiet {
			fmt.Fprintf(os.Stderr, "Note: cost budget for '%s': %s\n", inst.Title, r.Describe())
		}
	}
}
