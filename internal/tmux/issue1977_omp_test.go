package tmux

import "testing"

func TestIssue1977_OMPHasStatusPatterns(t *testing.T) {
	raw := DefaultRawPatterns("omp")
	if raw == nil {
		t.Fatal("DefaultRawPatterns(\"omp\") = nil, want omp-specific patterns")
	}
	if len(raw.BusyPatterns) == 0 {
		t.Error("omp has no busy patterns")
	}
	if len(raw.PromptPatterns) == 0 {
		t.Error("omp has no prompt patterns")
	}
}

func TestIssue1977_OMPReadinessUsesCapturedTUIStates(t *testing.T) {
	d := NewPromptDetector("omp")

	busy := "╭── Sonnet 5 · high ──╮\n⠋ Working… ⟨esc⟩"
	if d.HasPrompt(busy) {
		t.Fatal("busy omp pane was classified as waiting")
	}

	waiting := "Allow tool: bash\nCommand: echo hi\n\n  Approve\n   Deny"
	if !d.HasPrompt(waiting) {
		t.Fatal("omp approval prompt was not classified as waiting")
	}
}
