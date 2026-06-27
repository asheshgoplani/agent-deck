package ui

import (
	"testing"

	"github.com/asheshgoplani/agent-deck/internal/session"
)

func TestBudgetBarColor(t *testing.T) {
	cases := []struct {
		level session.BudgetLevel
		want  string // hex of expected color
	}{
		{session.BudgetNormal, string(ColorGreen)},
		{session.BudgetWarn, string(ColorYellow)},
		{session.BudgetHigh, string(ColorRed)},
		{session.BudgetOver, string(ColorRed)},
	}
	for _, tc := range cases {
		if got := string(budgetBarColor(tc.level)); got != tc.want {
			t.Errorf("budgetBarColor(%v) = %s, want %s", tc.level, got, tc.want)
		}
	}
}
