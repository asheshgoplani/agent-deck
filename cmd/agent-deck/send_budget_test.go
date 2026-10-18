package main

import (
	"os"
	"strings"
	"testing"
	"time"

	"github.com/asheshgoplani/agent-deck/internal/costs"
	"github.com/asheshgoplani/agent-deck/internal/sendqueue"
	"github.com/asheshgoplani/agent-deck/internal/session"
)

// C9 (prime-agent adoption): cost budgets are enforced on every send and
// wake. Each enforcement point has three cases: under budget passes, over
// budget is refused before anything is typed, and no budget configured is
// unchanged.

// TestQueuedSendBudgetGate covers the queued worker and `session queue
// release`: both type through typeQueued, which checks the budget at the
// typing boundary.
func TestQueuedSendBudgetGate(t *testing.T) {
	for _, tc := range []struct {
		name   string
		budget costs.BudgetResult
		typed  bool
	}{
		{"no budget configured", costs.BudgetResult{Action: costs.BudgetActionNone}, true},
		{"near budget", costs.BudgetResult{Action: costs.BudgetActionWarn, Reason: "daily global limit exceeded", UsedMicro: 9e6, LimitMicro: 10e6}, true},
		{"over budget", costs.BudgetResult{Action: costs.BudgetActionStop, Reason: "daily global limit exceeded", UsedMicro: 11e6, LimitMicro: 10e6}, false},
	} {
		for _, status := range []string{"waiting", queueReleaseStatus} {
			t.Run(tc.name+"/"+status, func(t *testing.T) {
				dir, rec := queueControlFixture(t, sendqueue.StateQueued)
				prevBudget, prevChild, prevRelease := budgetForSend, sendChild, sendChildRelease
				t.Cleanup(func() { budgetForSend, sendChild, sendChildRelease = prevBudget, prevChild, prevRelease })
				budgetForSend = func(_ string, target *session.Instance, _ []*session.Instance, _ *session.Storage) costs.BudgetResult {
					if target == nil || target.ID != rec.SessionID {
						t.Fatalf("budget checked for %+v, want %q", target, rec.SessionID)
					}
					return tc.budget
				}
				typed := false
				child := func(_, _, _, resultPath string) (int, func() int, error) {
					typed = true
					_ = os.WriteFile(resultPath, []byte(`{"success":true,"delivery":"delivered","submitted":true,"confirmation":"confirmed"}`), 0o600)
					return 4242, func() int { return 0 }, nil
				}
				sendChild, sendChildRelease = child, child
				set := func(fn func(*sendqueue.Record)) error {
					stored, err := sendqueue.Update(dir, rec.SendID, time.Now(), fn)
					if err == nil {
						*rec = *stored
					}
					return err
				}
				if !typeQueued("", dir, rec, &session.Instance{ID: rec.SessionID}, nil, status, "", 0, set) {
					t.Fatal("typeQueued could not record the send")
				}
				if typed != tc.typed {
					t.Fatalf("typed = %v, want %v (record %+v)", typed, tc.typed, rec)
				}
				stored, err := sendqueue.Load(dir, rec.SendID)
				if err != nil {
					t.Fatal(err)
				}
				if tc.typed {
					if stored.State != sendqueue.StateSubmitted {
						t.Fatalf("state = %q, want submitted", stored.State)
					}
					return
				}
				if stored.State != sendqueue.StateFailed || stored.Attempts != 0 || stored.SentAt != "" ||
					!strings.Contains(stored.Reason, "budget exceeded: daily global limit exceeded ($11.00 used of $10.00); nothing was typed") {
					t.Fatalf("over-budget record: %+v", stored)
				}
			})
		}
	}
}
