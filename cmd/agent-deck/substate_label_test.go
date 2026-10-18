package main

import (
	"testing"

	"github.com/asheshgoplani/agent-deck/internal/session"
)

// The transcript-derived limit kinds must each read as their own condition:
// a blocked account and a per-model cap need different fixes from a plan
// window, so a supervisor has to be able to tell them apart from the label.
func TestSubstateLabel_TranscriptLimitKinds(t *testing.T) {
	tests := []struct {
		sub  session.Substate
		want string
	}{
		{session.SubstateUsageLimit, "usage limit"},
		{session.SubstateModelLimit, "model limit"},
		{session.SubstateAccountBlocked, "account blocked (org disabled)"},
	}
	for _, tt := range tests {
		if got := SubstateLabel(tt.sub); got != tt.want {
			t.Errorf("SubstateLabel(%q) = %q, want %q", tt.sub, got, tt.want)
		}
	}
}
