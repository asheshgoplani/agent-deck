package session

import "testing"

func TestGetContextBudget_DefaultsWhenUnset(t *testing.T) {
	c := &UserConfig{} // no [context_budget] section present
	got := c.GetContextBudget()

	if !got.GetEnabled() {
		t.Errorf("Enabled default = false, want true")
	}
	if !got.GetAutonomousHandoff() {
		t.Errorf("AutonomousHandoff default = false, want true")
	}
	if got.WarnTokens != 150000 {
		t.Errorf("WarnTokens = %d, want 150000", got.WarnTokens)
	}
	if got.HighTokens != 200000 {
		t.Errorf("HighTokens = %d, want 200000", got.HighTokens)
	}
	if got.CeilingTokens != 250000 {
		t.Errorf("CeilingTokens = %d, want 250000", got.CeilingTokens)
	}
	if got.HandoffTimeoutSeconds != 300 {
		t.Errorf("HandoffTimeoutSeconds = %d, want 300", got.HandoffTimeoutSeconds)
	}
}

func TestGetContextBudget_RespectsExplicitValues(t *testing.T) {
	disabled := false
	c := &UserConfig{ContextBudget: ContextBudgetSettings{
		Enabled:               &disabled,
		WarnTokens:            100000,
		HighTokens:            120000,
		CeilingTokens:         140000,
		HandoffTimeoutSeconds: 60,
	}}
	got := c.GetContextBudget()

	if got.GetEnabled() {
		t.Errorf("Enabled = true, want false (explicit)")
	}
	if got.WarnTokens != 100000 || got.HighTokens != 120000 || got.CeilingTokens != 140000 {
		t.Errorf("explicit thresholds not preserved: %+v", got)
	}
	if got.HandoffTimeoutSeconds != 60 {
		t.Errorf("HandoffTimeoutSeconds = %d, want 60", got.HandoffTimeoutSeconds)
	}
	// AutonomousHandoff left nil -> defaults to true even when other fields explicit.
	if !got.GetAutonomousHandoff() {
		t.Errorf("AutonomousHandoff default = false, want true")
	}
}
