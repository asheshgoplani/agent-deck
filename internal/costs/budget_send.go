package costs

import (
	"fmt"
	"strings"
)

// Enforced reports whether the limits gate sends and wakes: enforce is on
// and at least one limit is set. Otherwise the send and wake gates do not
// open the cost store at all, and budgets stay display-only.
func (c BudgetConfig) Enforced() bool {
	if !c.Enforce {
		return false
	}
	if c.DailyLimit > 0 || c.WeeklyLimit > 0 || c.MonthlyLimit > 0 {
		return true
	}
	for _, limit := range c.GroupLimits {
		if limit > 0 {
			return true
		}
	}
	for _, limit := range c.SessionLimits {
		if limit > 0 {
			return true
		}
	}
	return false
}

// Enforced reports whether this checker's limits gate sends and wakes.
func (b *BudgetChecker) Enforced() bool { return b != nil && b.cfg.Enforced() }

// CheckSend evaluates every limit that applies to a send or wake aimed at
// sessionID, in one read transaction, with the same sums as CheckTx.
func (b *BudgetChecker) CheckSend(sessionID, groupName string, groupSessionIDs []string) (BudgetResult, error) {
	tx, err := b.store.DB().Begin()
	if err != nil {
		return BudgetResult{Action: BudgetActionNone}, err
	}
	defer func() { _ = tx.Rollback() }()
	return b.CheckTx(tx, sessionID, groupName, groupSessionIDs)
}

// Describe is the human reason for a warn or stop result, for example
// "daily global limit exceeded ($51.00 used of $50.00)".
func (r BudgetResult) Describe() string {
	reason := r.Reason
	if r.Action == BudgetActionWarn {
		reason = strings.Replace(reason, "exceeded", "almost reached", 1)
	}
	return fmt.Sprintf("%s (%s used of %s)", reason, FormatUSD(r.UsedMicro), FormatUSD(r.LimitMicro))
}

// UsedUSD is the spend counted against the limit, in dollars.
func (r BudgetResult) UsedUSD() float64 { return microToUSD(r.UsedMicro) }

// LimitUSD is the limit that was checked, in dollars.
func (r BudgetResult) LimitUSD() float64 { return microToUSD(r.LimitMicro) }
