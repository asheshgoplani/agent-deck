package ui

import (
	"strings"
	"testing"

	"github.com/asheshgoplani/agent-deck/internal/session"
)

func TestBudgetBadge(t *testing.T) {
	if got := budgetBadge(session.BudgetNormal, false); got != "" {
		t.Errorf("normal badge = %q, want empty", got)
	}
	for _, lvl := range []session.BudgetLevel{session.BudgetWarn, session.BudgetHigh, session.BudgetOver} {
		got := budgetBadge(lvl, false)
		if got == "" {
			t.Errorf("level %v produced empty badge", lvl)
		}
		if !strings.Contains(got, lvl.String()) {
			t.Errorf("badge %q does not contain level name %q", got, lvl.String())
		}
	}
}
