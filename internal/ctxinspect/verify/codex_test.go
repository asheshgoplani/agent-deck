package verify

import (
	"errors"
	"testing"
)

// codexStatusPane is a Codex CLI /status panel: workspace and model metadata,
// cumulative session token usage, and one context-window occupancy line. There
// is no per-category breakdown, which is the whole reason Codex parity is a
// single row.
const codexStatusPane = `
/status

  📂 Workspace
    • Path: /Users/example/project
    • Approval Mode: on-request
  🧠 Model
    • Name: gpt-5.1-codex
    • Provider: OpenAI
  📊 Token Usage
    • Session ID: 019fa876-1234-7890-abcd-000000000000
    • Input: 21,579
    • Output: 1,204
    • Total: 22,783
    • Context Window: 258,400
    • Context Used: 21,579
`

func TestParseCodexStatus(t *testing.T) {
	h, err := ParseCodexStatus(codexStatusPane)
	if err != nil {
		t.Fatalf("ParseCodexStatus: %v", err)
	}
	if h.Harness != "codex" || h.Command != CodexCommand {
		t.Fatalf("identity = %s/%s, want codex//status", h.Harness, h.Command)
	}
	if h.Model != "gpt-5.1-codex" {
		t.Errorf("Model = %q, want gpt-5.1-codex", h.Model)
	}
	if h.Window != 258400 {
		t.Errorf("Window = %d, want 258400", h.Window)
	}
	if h.Used != 21579 || h.UsedDerived {
		t.Errorf("Used = %d (derived=%v), want 21579 read directly", h.Used, h.UsedDerived)
	}
	if f, ok := h.Figure(codexLabelTotal); !ok || f.Tokens != 22783 {
		t.Errorf("cumulative total = %+v, want 22783", f)
	}
}

// TestParseCodexStatusDerivesOccupancyFromPercentLeft covers the versions that
// print only "N% context left". The derived figure is marked derived, so the
// comparison can widen its tolerance instead of pretending to a precision the
// panel never published.
func TestParseCodexStatusDerivesOccupancyFromPercentLeft(t *testing.T) {
	pane := `
  📊 Token Usage
    • Input: 21,579
    • Output: 1,204
    • Context Window: 200,000
  92% context left
`
	h, err := ParseCodexStatus(pane)
	if err != nil {
		t.Fatalf("ParseCodexStatus: %v", err)
	}
	if !h.UsedDerived {
		t.Fatal("an occupancy computed from a percentage must be marked derived")
	}
	if h.Used != 16000 {
		t.Errorf("Used = %d, want 16000 (8%% of 200000)", h.Used)
	}
	if h.UsedSlack != 1000 {
		t.Errorf("UsedSlack = %d, want 1000 (half a printed percent of the window)", h.UsedSlack)
	}
}

func TestParseCodexStatusRejectsAPaneWithoutThePanel(t *testing.T) {
	h, err := ParseCodexStatus("codex> nothing to see here")
	if !errors.Is(err, ErrNoAccounting) {
		t.Fatalf("err = %v, want ErrNoAccounting", err)
	}
	if h != nil {
		t.Fatal("a failed parse must not return a report")
	}
}

// TestCodexParityComparesOnlyOccupancy is the honest-degradation contract for
// Codex: the cumulative input/output/total rows are a different quantity from a
// fixed prefix, and diffing them would be a category error dressed up as a
// number.
func TestCodexParityComparesOnlyOccupancy(t *testing.T) {
	groups := codexGroups()
	if len(groups) != 1 {
		t.Fatalf("codex declares %d groups, want exactly the occupancy group", len(groups))
	}
	g := groups[0]
	if !g.HarnessUsed {
		t.Error("the codex group must take its harness side from the occupancy figure")
	}
	if len(g.HarnessLabels) != 0 {
		t.Errorf("the codex group must claim no labelled rows, got %v", g.HarnessLabels)
	}
}

func TestCodexReadyNeedsMoreThanOneRow(t *testing.T) {
	if codexReady("  • Input: 21,579") {
		t.Error("a single row is not a rendered /status panel")
	}
	if !codexReady(codexStatusPane) {
		t.Error("a complete panel must be ready")
	}
}
