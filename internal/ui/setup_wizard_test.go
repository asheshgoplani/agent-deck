package ui

import (
	"testing"

	tea "github.com/charmbracelet/bubbletea"
)

func TestSetupWizard_InitialState(t *testing.T) {
	wizard := NewSetupWizard()

	if wizard.IsVisible() {
		t.Error("Wizard should not be visible initially")
	}

	wizard.Show()
	if !wizard.IsVisible() {
		t.Error("Wizard should be visible after Show()")
	}

	if wizard.currentStep != 0 {
		t.Errorf("currentStep: got %d, want 0", wizard.currentStep)
	}
}

func TestSetupWizard_StepNavigation(t *testing.T) {
	wizard := NewSetupWizard()
	wizard.Show()

	// Initial step is 0 (welcome)
	if wizard.currentStep != 0 {
		t.Errorf("Initial step: got %d, want 0", wizard.currentStep)
	}

	// Go to next step
	wizard.nextStep()
	if wizard.currentStep != 1 {
		t.Errorf("After next: got %d, want 1", wizard.currentStep)
	}

	// Go back
	wizard.prevStep()
	if wizard.currentStep != 0 {
		t.Errorf("After prev: got %d, want 0", wizard.currentStep)
	}

	// Can't go before 0
	wizard.prevStep()
	if wizard.currentStep != 0 {
		t.Errorf("Should stay at 0: got %d", wizard.currentStep)
	}
}

func TestSetupWizard_GetConfig(t *testing.T) {
	wizard := NewSetupWizard()
	wizard.Show()

	// Set some values
	wizard.selectedTool = 0 // Claude
	wizard.dangerousMode = false
	wizard.useDefaultConfigDir = true

	config := wizard.GetConfig()

	if config.DefaultTool != "claude" {
		t.Errorf("DefaultTool: got %q, want %q", config.DefaultTool, "claude")
	}
	if config.Claude.GetDangerousMode() != false {
		t.Error("DangerousMode should be false")
	}
}

func TestSetupWizard_SkipClaudeSettingsForNonClaude(t *testing.T) {
	wizard := NewSetupWizard()
	wizard.Show()

	// Select Gemini (index 1)
	wizard.selectedTool = 1
	wizard.currentStep = 1 // On tool selection

	// Move to next - should skip Claude settings (step 2) and go to Ready (step 3)
	wizard.nextStep()
	if wizard.currentStep != 3 {
		t.Errorf("Should skip to step 3 for non-Claude tool: got %d", wizard.currentStep)
	}

	// Go back should also skip Claude settings
	wizard.prevStep()
	if wizard.currentStep != 1 {
		t.Errorf("Should go back to step 1: got %d", wizard.currentStep)
	}
}

func TestSetupWizard_ClaudeSettingsForClaude(t *testing.T) {
	wizard := NewSetupWizard()
	wizard.Show()

	// Select Claude (index 0)
	wizard.selectedTool = 0
	wizard.currentStep = 1 // On tool selection

	// Move to next - should go to Claude settings (step 2)
	wizard.nextStep()
	if wizard.currentStep != 2 {
		t.Errorf("Should go to Claude settings (step 2): got %d", wizard.currentStep)
	}

	// Go to next again - should go to Ready (step 3)
	wizard.nextStep()
	if wizard.currentStep != 3 {
		t.Errorf("Should go to Ready (step 3): got %d", wizard.currentStep)
	}
}

func TestSetupWizard_IsComplete(t *testing.T) {
	wizard := NewSetupWizard()
	wizard.Show()

	// Not complete initially
	if wizard.IsComplete() {
		t.Error("Should not be complete initially")
	}

	// Navigate to Ready step
	wizard.currentStep = 3

	// Still not complete until user confirms
	if wizard.IsComplete() {
		t.Error("Should not be complete until confirmed")
	}

	// Confirm completion
	wizard.complete = true
	if !wizard.IsComplete() {
		t.Error("Should be complete after confirmation")
	}
}

func TestSetupWizard_ToolOptions(t *testing.T) {
	wizard := NewSetupWizard()

	// Verify tool options
	expectedTools := []string{"claude", "gemini", "opencode", "codex", "pi", "shell"}
	if len(wizard.toolOptions) != len(expectedTools) {
		t.Errorf("Tool options count: got %d, want %d", len(wizard.toolOptions), len(expectedTools))
	}

	for i, expected := range expectedTools {
		if wizard.toolOptions[i] != expected {
			t.Errorf("Tool option %d: got %q, want %q", i, wizard.toolOptions[i], expected)
		}
	}
}

func TestSetupWizard_DefaultValues(t *testing.T) {
	wizard := NewSetupWizard()

	// Check defaults
	if wizard.selectedTool != 0 {
		t.Errorf("Default selectedTool: got %d, want 0 (Claude)", wizard.selectedTool)
	}
	if wizard.dangerousMode != false {
		t.Error("Default dangerousMode should be false")
	}
	if wizard.useDefaultConfigDir != true {
		t.Error("Default useDefaultConfigDir should be true")
	}
}

func TestSetupWizard_Hide(t *testing.T) {
	wizard := NewSetupWizard()
	wizard.Show()

	if !wizard.IsVisible() {
		t.Error("Should be visible after Show()")
	}

	wizard.Hide()
	if wizard.IsVisible() {
		t.Error("Should not be visible after Hide()")
	}
}

func TestSetupWizard_GetConfigWithCustomConfigDir(t *testing.T) {
	wizard := NewSetupWizard()
	wizard.Show()

	// Set custom config dir
	wizard.selectedTool = 0 // Claude
	wizard.useDefaultConfigDir = false
	wizard.customConfigDir = "~/.claude-work"

	config := wizard.GetConfig()

	if config.Claude.ConfigDir != "~/.claude-work" {
		t.Errorf("ConfigDir: got %q, want %q", config.Claude.ConfigDir, "~/.claude-work")
	}
}

func TestSetupWizard_GetConfigWithDangerousMode(t *testing.T) {
	wizard := NewSetupWizard()
	wizard.Show()

	// Enable dangerous mode
	wizard.selectedTool = 0 // Claude
	wizard.dangerousMode = true

	config := wizard.GetConfig()

	if config.Claude.GetDangerousMode() != true {
		t.Error("DangerousMode should be true")
	}
}

func TestSetupWizard_NonClaudeToolConfig(t *testing.T) {
	wizard := NewSetupWizard()
	wizard.Show()

	// Select Gemini
	wizard.selectedTool = 1

	config := wizard.GetConfig()

	if config.DefaultTool != "gemini" {
		t.Errorf("DefaultTool: got %q, want %q", config.DefaultTool, "gemini")
	}

	// Claude settings should use defaults (not overwritten)
	if config.Claude.GetDangerousMode() != false {
		t.Error("DangerousMode should be false for non-Claude")
	}
}

func TestSetupWizard_StepMaxBounds(t *testing.T) {
	wizard := NewSetupWizard()
	wizard.Show()

	// Go to last step
	wizard.currentStep = 3

	// Try to go beyond last step
	wizard.nextStep()
	if wizard.currentStep != 3 {
		t.Errorf("Should stay at step 3: got %d", wizard.currentStep)
	}
}

func TestSetupWizard_UpdateKeyHandling(t *testing.T) {
	wizard := NewSetupWizard()
	wizard.Show()
	wizard.SetSize(80, 24)

	// Enter at welcome should go to step 1
	wizard.Update(tea.KeyMsg{Type: tea.KeyEnter})
	if wizard.currentStep != 1 {
		t.Errorf("Enter at welcome: got step %d, want 1", wizard.currentStep)
	}

	// Down key should change tool selection
	wizard.Update(tea.KeyMsg{Type: tea.KeyDown})
	if wizard.selectedTool != 1 {
		t.Errorf("Down key: got selectedTool %d, want 1", wizard.selectedTool)
	}

	// Up key should go back
	wizard.Update(tea.KeyMsg{Type: tea.KeyUp})
	if wizard.selectedTool != 0 {
		t.Errorf("Up key: got selectedTool %d, want 0", wizard.selectedTool)
	}

	// Esc should go back to step 0
	wizard.Update(tea.KeyMsg{Type: tea.KeyEsc})
	if wizard.currentStep != 0 {
		t.Errorf("Esc: got step %d, want 0", wizard.currentStep)
	}
}

func TestSetupWizard_ViewNonEmpty(t *testing.T) {
	wizard := NewSetupWizard()
	wizard.SetSize(80, 24)

	// When not visible, View should return empty
	view := wizard.View()
	if view != "" {
		t.Error("View should be empty when not visible")
	}

	// When visible, View should return non-empty
	wizard.Show()
	view = wizard.View()
	if view == "" {
		t.Error("View should be non-empty when visible")
	}
}

func TestSetupWizard_EscOnWelcomeCompletes(t *testing.T) {
	wizard := NewSetupWizard()
	wizard.Show()
	wizard.SetSize(80, 24)

	// Esc on welcome step should complete the wizard with defaults
	wizard.Update(tea.KeyMsg{Type: tea.KeyEsc})
	if !wizard.IsComplete() {
		t.Error("Esc on welcome step should complete the wizard")
	}

	// Config should have sensible defaults
	config := wizard.GetConfig()
	if config.DefaultTool != "claude" {
		t.Errorf("Default tool should be 'claude', got %q", config.DefaultTool)
	}
}

func TestSetupWizard_EscOnNonWelcomeGoesBack(t *testing.T) {
	wizard := NewSetupWizard()
	wizard.Show()
	wizard.SetSize(80, 24)

	// Advance to tool selection
	wizard.Update(tea.KeyMsg{Type: tea.KeyEnter})
	if wizard.currentStep != 1 {
		t.Fatalf("Expected step 1 after Enter, got %d", wizard.currentStep)
	}

	// Esc on tool selection should go back to welcome, not complete
	wizard.Update(tea.KeyMsg{Type: tea.KeyEsc})
	if wizard.currentStep != 0 {
		t.Errorf("Esc on tool selection: got step %d, want 0", wizard.currentStep)
	}
	if wizard.IsComplete() {
		t.Error("Esc on non-welcome step should not complete the wizard")
	}
}

func TestSetupWizard_GetConfig_DefaultTheme(t *testing.T) {
	wizard := NewSetupWizard()
	config := wizard.GetConfig()

	if config.Theme != "dark" {
		t.Errorf("Default theme should be 'dark', got %q", config.Theme)
	}
}
