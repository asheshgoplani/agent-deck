package health

import (
	"testing"
	"time"
)

func TestBudgetWarningTmuxCallsNeedSustainedBreach(t *testing.T) {
	ResetStatusPassBreachState()
	t.Cleanup(ResetStatusPassBreachState)

	// One Ctrl+Q-sized spike must not flash the footer. A pass with no
	// sessions is not a ratio, so the fixed cache probes do not warn either.
	if got := BudgetWarning(10*time.Millisecond, 26, 57); got != "" {
		t.Fatalf("single overage warned: %q", got)
	}
	if got := BudgetWarning(10*time.Millisecond, 0, 2); got != "" {
		t.Fatalf("zero-session pass warned: %q", got)
	}
	if got := BudgetWarning(10*time.Millisecond, 26, 40); got != "" {
		t.Fatalf("under-budget pass warned: %q", got)
	}

	for i := 0; i < 2; i++ {
		if got := BudgetWarning(10*time.Millisecond, 26, 57); got != "" {
			t.Fatalf("breach %d warned early: %q", i+1, got)
		}
	}
	got := BudgetWarning(10*time.Millisecond, 26, 57)
	if got != "Health: tmux calls exceed twice the session count" {
		t.Fatalf("three consecutive breaches: got %q", got)
	}

	// One sample back under budget clears it, same as the status-pass timer.
	if got := BudgetWarning(10*time.Millisecond, 26, 5); got != "" {
		t.Fatalf("recovery left warning set: %q", got)
	}
	if got := BudgetWarning(10*time.Millisecond, 26, 57); got != "" {
		t.Fatalf("warning returned on the first sample after recovery: %q", got)
	}
}
