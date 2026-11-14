package main

import (
	"reflect"
	"testing"

	"github.com/asheshgoplani/agent-deck/internal/session"
)

// budgetForSend is the cmd test seam over the one gate the daemon also
// calls. The branch table lives in internal/session/wake_budget_test.go and
// runs against session.WakeBudget; this pins that the CLI uses that same
// function, so the table covers the CLI.
func TestBudgetForSend_IsSessionWakeBudget(t *testing.T) {
	if reflect.ValueOf(budgetForSend).Pointer() != reflect.ValueOf(session.WakeBudget).Pointer() {
		t.Fatal("budgetForSend is not session.WakeBudget: the CLI gate and the daemon gate have diverged")
	}
}
