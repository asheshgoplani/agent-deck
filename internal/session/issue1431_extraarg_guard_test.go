package session

import "testing"

// Issue #1431(b): `agent-deck launch --extra-arg "--model opus"` silently
// broke tmux bringup. A single --extra-arg token is shell-quoted as ONE
// argument, so "--model opus" reaches claude as the literal single arg
// '--model opus' (with an embedded space) — an unknown flag that makes claude
// exit immediately, leaving a dead pane the registry still reports as running.
//
// ValidateExtraArgToken turns that silent failure into an actionable
// error at spawn time: a token that looks like a flag (starts with '-') AND
// contains whitespace is almost always a flag mashed together with its value
// that the user meant as two separate --extra-arg tokens.

func TestValidateExtraArgToken_RejectsFlagMashedWithValue(t *testing.T) {
	bad := []string{
		"--model opus",
		"--model claude-opus-4-8",
		"-c something",
		"--permission-mode auto",
	}
	for _, tok := range bad {
		if err := ValidateExtraArgToken(tok); err == nil {
			t.Errorf("ValidateExtraArgToken(%q) = nil, want error (flag mashed with value)", tok)
		}
	}
}

func TestValidateExtraArgToken_AllowsCleanTokens(t *testing.T) {
	good := []string{
		"--model",                        // bare flag
		"opus",                           // bare value
		"--dangerously-skip-permissions", // bare flag, no value
		"claude-opus-4-8",                // value with dashes but no space
		"be concise please",              // a value with spaces that is NOT a flag
		"",                               // empty (handled elsewhere; not a footgun)
	}
	for _, tok := range good {
		if err := ValidateExtraArgToken(tok); err != nil {
			t.Errorf("ValidateExtraArgToken(%q) = %v, want nil", tok, err)
		}
	}
}
