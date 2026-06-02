package session

import (
	"testing"
)

// TestHermesConductorSpec_Exists verifies that GetConductorAgentSpec("hermes") returns no error.
func TestHermesConductorSpec_Exists(t *testing.T) {
	_, err := GetConductorAgentSpec("hermes")
	if err != nil {
		t.Fatalf("expected no error for hermes spec, got: %v", err)
	}
}

// TestHermesConductorSpec_SupportsClearOnCompact verifies that the Hermes spec has SupportsClearOnCompact == true.
func TestHermesConductorSpec_SupportsClearOnCompact(t *testing.T) {
	spec, err := GetConductorAgentSpec("hermes")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !spec.SupportsClearOnCompact {
		t.Errorf("expected SupportsClearOnCompact=true for hermes spec, got false")
	}
}

// TestHermesConductorSpec_DefaultCommand verifies that the Hermes spec has DefaultCommand == "hermes".
func TestHermesConductorSpec_DefaultCommand(t *testing.T) {
	spec, err := GetConductorAgentSpec("hermes")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if spec.DefaultCommand != "hermes" {
		t.Errorf("expected DefaultCommand=%q, got %q", "hermes", spec.DefaultCommand)
	}
}

// TestHermesConductorSpec_InstructionsFileName verifies that the Hermes spec has InstructionsFileName == "HERMES.md".
func TestHermesConductorSpec_InstructionsFileName(t *testing.T) {
	spec, err := GetConductorAgentSpec("hermes")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if spec.InstructionsFileName != "HERMES.md" {
		t.Errorf("expected InstructionsFileName=%q, got %q", "HERMES.md", spec.InstructionsFileName)
	}
}

// TestHermesConductorSpec_DisplayName verifies that the Hermes spec has DisplayName == "Hermes".
func TestHermesConductorSpec_DisplayName(t *testing.T) {
	spec, err := GetConductorAgentSpec("hermes")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if spec.DisplayName != "Hermes" {
		t.Errorf("expected DisplayName=%q, got %q", "Hermes", spec.DisplayName)
	}
}

// TestHermesGetClearOnCompact_NilDefaultsToTrue verifies that a Hermes ConductorMeta with
// ClearOnCompact == nil defaults to returning true from GetClearOnCompact.
func TestHermesGetClearOnCompact_NilDefaultsToTrue(t *testing.T) {
	meta := &ConductorMeta{Agent: "hermes", ClearOnCompact: nil}
	if !meta.GetClearOnCompact() {
		t.Errorf("expected GetClearOnCompact()=true when ClearOnCompact is nil, got false")
	}
}

// TestHermesGetClearOnCompact_ExplicitFalse verifies that an explicit false pointer returns false.
func TestHermesGetClearOnCompact_ExplicitFalse(t *testing.T) {
	f := false
	meta := &ConductorMeta{Agent: "hermes", ClearOnCompact: &f}
	if meta.GetClearOnCompact() {
		t.Errorf("expected GetClearOnCompact()=false when ClearOnCompact is explicitly false, got true")
	}
}

// TestHermesGetClearOnCompact_ExplicitTrue verifies that an explicit true pointer returns true.
func TestHermesGetClearOnCompact_ExplicitTrue(t *testing.T) {
	tr := true
	meta := &ConductorMeta{Agent: "hermes", ClearOnCompact: &tr}
	if !meta.GetClearOnCompact() {
		t.Errorf("expected GetClearOnCompact()=true when ClearOnCompact is explicitly true, got false")
	}
}

// TestHermesGroupSettings_FieldsAreExported verifies that GroupHermesSettings has exported fields
// that can be assigned and read back via struct literals.
func TestHermesGroupSettings_FieldsAreExported(t *testing.T) {
	s := GroupHermesSettings{
		Command:    "hermes-custom",
		GatewayURL: "ws://localhost:9999",
		YoloMode:   true,
	}
	if s.Command != "hermes-custom" {
		t.Errorf("expected Command=%q, got %q", "hermes-custom", s.Command)
	}
	if s.GatewayURL != "ws://localhost:9999" {
		t.Errorf("expected GatewayURL=%q, got %q", "ws://localhost:9999", s.GatewayURL)
	}
	if !s.YoloMode {
		t.Errorf("expected YoloMode=true, got false")
	}
}

// TestHermesConductorSettings_FieldsAreExported verifies that ConductorHermesSettings has exported
// fields that can be assigned and read back via struct literals.
func TestHermesConductorSettings_FieldsAreExported(t *testing.T) {
	s := ConductorHermesSettings{
		Command:    "hermes-custom",
		GatewayURL: "ws://localhost:9999",
		YoloMode:   true,
	}
	if s.Command != "hermes-custom" {
		t.Errorf("expected Command=%q, got %q", "hermes-custom", s.Command)
	}
	if s.GatewayURL != "ws://localhost:9999" {
		t.Errorf("expected GatewayURL=%q, got %q", "ws://localhost:9999", s.GatewayURL)
	}
	if !s.YoloMode {
		t.Errorf("expected YoloMode=true, got false")
	}
}
