package web

import (
	"slices"
	"testing"
)

// Review M1: an operator's Command Center ask is a person's message, so an
// enforced cost budget never blocks it.
func TestCommandCenterAskIgnoresTheBudget(t *testing.T) {
	args := commandCenterSendArgs("work", "conductor-x", "[command-center] hi")
	if !slices.Contains(args, "--ignore-budget") {
		t.Fatalf("command center send args = %v, want --ignore-budget", args)
	}
	if args[0] != "-p" || args[1] != "work" || args[2] != "session" || args[3] != "send" || args[4] != "conductor-x" {
		t.Fatalf("args = %v", args)
	}
}
