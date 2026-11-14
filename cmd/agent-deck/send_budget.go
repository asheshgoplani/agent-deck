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

// ignoreBudgetFlagHelp is the help of every --ignore-budget flag.
const ignoreBudgetFlagHelp = "Send even when an enforced [costs.budgets] limit is spent (for messages a person starts)"

// budgetForSend is the cost budget gate for agent-initiated sends and
// wakes: `session send` (and so goal nudges, conductor heartbeats and inbox
// wakes), the queued send worker and `session queue release`, `launch -m`,
// `session start -m` and the MCP restart's "continue". target is the
// session the text goes to, instances the profile's sessions as the caller
// loaded them (for group membership). storage may be nil; the profile's
// storage is then opened for the cost store only. Tests replace it.
var budgetForSend = session.WakeBudget

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
		session.PublishBudgetExceeded(profile, inst.ID, via, r)
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
