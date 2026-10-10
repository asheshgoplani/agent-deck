package costs

import (
	"fmt"
	"strings"
)

// Enforced reports whether any limit is set. With none, the send and wake
// gates do not open the cost store at all, so behavior is unchanged.
func (c BudgetConfig) Enforced() bool {
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
	return fmt.Sprintf("%s ($%.2f used of $%.2f)", reason, float64(r.UsedMicro)/1e6, float64(r.LimitMicro)/1e6)
}
