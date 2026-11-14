package session

import (
	"strings"
	"testing"
)

// The identity block told every child that a question "wakes" its parent.
// For a parent that is not a conductor and did not ask, nothing did. The
// block now says who is woken, and still fits the line budget.
func TestIssue3131_IdentityBlockSaysWhoIsWoken(t *testing.T) {
	identityTestEnv(t)
	inst := identityTestInstance("claude")
	got := inst.BuildIdentityPrompt()
	if strings.Contains(got, "that wakes it") {
		t.Errorf("the block must not promise a wake to every parent:\n%s", got)
	}
	for _, want := range []string{
		"Your parent then receives one urgent record",
		"start a line with NEED:",
		"wakes an idle parent that is a conductor, that sent you the message you are answering, or that set `[inbox] wake_parents`",
		"any other parent reads it on its next turn",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("identity block missing %q:\n%s", want, got)
		}
	}
}
