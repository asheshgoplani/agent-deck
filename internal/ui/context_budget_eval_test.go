package ui

import (
	"testing"

	"github.com/asheshgoplani/agent-deck/internal/session"
)

func TestShouldNotifyBudgetCrossing(t *testing.T) {
	cases := []struct {
		prev, cur session.BudgetLevel
		want      bool
	}{
		{session.BudgetNormal, session.BudgetWarn, false}, // warn is bar/badge only
		{session.BudgetWarn, session.BudgetHigh, true},    // first cross into high
		{session.BudgetHigh, session.BudgetHigh, false},   // no re-fire
		{session.BudgetHigh, session.BudgetOver, true},    // escalate to over
		{session.BudgetOver, session.BudgetHigh, false},   // dropping back: no fire
		{session.BudgetHigh, session.BudgetWarn, false},   // dropping back
	}
	for _, tc := range cases {
		if got := shouldNotifyBudgetCrossing(tc.prev, tc.cur); got != tc.want {
			t.Errorf("shouldNotifyBudgetCrossing(%v,%v) = %v, want %v", tc.prev, tc.cur, got, tc.want)
		}
	}
}
