package session

import "testing"

// TestSetFieldTitleClearsAutoName pins that renaming via the central SetField
// mutator clears the AutoName flag, so a user-chosen name sticks (the TUI then
// shows the typed name verbatim instead of the live pane title).
func TestSetFieldTitleClearsAutoName(t *testing.T) {
	inst := NewInstance("lively-fjord", t.TempDir())
	inst.AutoName = true

	if _, _, err := SetField(inst, FieldTitle, "my-feature", nil); err != nil {
		t.Fatalf("SetField(title): %v", err)
	}

	if inst.Title != "my-feature" {
		t.Errorf("Title = %q, want %q", inst.Title, "my-feature")
	}
	if inst.AutoName {
		t.Errorf("AutoName = true after rename, want false")
	}
}
