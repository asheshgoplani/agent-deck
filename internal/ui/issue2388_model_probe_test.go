package ui

import (
	"os"
	"path/filepath"
	"slices"
	"testing"

	"github.com/asheshgoplani/agent-deck/internal/session"
)

// Issue #2388: the TUI new-session dialog reads the probed model catalog.
// A fake `codex` stub on PATH answers `codex debug models`; nothing here runs
// the real CLI.

const issue2388FakeModels = `{"models":[
 {"slug":"gpt-7-nova","visibility":"list","priority":1,"supported_reasoning_levels":[{"effort":"low"},{"effort":"hyper"}]},
 {"slug":"gpt-5.5","visibility":"list","priority":2,"supported_reasoning_levels":[{"effort":"low"},{"effort":"medium"}]}
]}`

func installIssue2388FakeCodex(t *testing.T) {
	t.Helper()
	dir := t.TempDir()
	fixture := filepath.Join(dir, "models.json")
	if err := os.WriteFile(fixture, []byte(issue2388FakeModels), 0o644); err != nil {
		t.Fatal(err)
	}
	script := "#!/bin/sh\nif [ \"$1\" = \"--version\" ]; then echo 'codex-cli 0.0.1'; exit 0; fi\ncat '" + fixture + "'\n"
	if err := os.WriteFile(filepath.Join(dir, "codex"), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+"/usr/bin"+string(os.PathListSeparator)+"/bin")
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	t.Setenv("AGENTDECK_TEST_MODEL_PROBE", "1")
	session.ResetModelProbeMemo()
	t.Cleanup(session.ResetModelProbeMemo)
}

func TestIssue2388_DialogUsesProbedCodexCatalog(t *testing.T) {
	installIssue2388FakeCodex(t)

	d := NewNewDialog()
	d.SetDefaultTool("codex")
	d.SetSize(100, 50)
	d.Show()
	d.filterModelSuggestions()

	if len(d.modelSuggestions) == 0 || d.modelSuggestions[0] != "gpt-7-nova" {
		t.Fatalf("model suggestions = %v, want the probed gpt-7-nova first", d.modelSuggestions)
	}
	if !slices.Contains(d.modelSuggestions, "gpt-6-astra") {
		t.Fatal("static catalog entries must stay suggested under a probe")
	}

	choices := d.reasoningEffortChoices()
	if !slices.Contains(choices, "hyper") || !slices.Contains(choices, "ultra") {
		t.Fatalf("effort choices = %v, want static efforts plus the probed hyper", choices)
	}

	d.modelInput.SetValue("gpt-5.5")
	if got := d.reasoningEffortChoices(); !slices.Equal(got, []string{"", "low", "medium"}) {
		t.Fatalf("effort choices for gpt-5.5 = %v, want the probed per-model list", got)
	}
}

func TestIssue2388_ProbeOffKeepsStaticDialog(t *testing.T) {
	installIssue2388FakeCodex(t)
	t.Setenv("AGENTDECK_TEST_MODEL_PROBE", "")

	d := NewNewDialog()
	d.SetDefaultTool("codex")
	d.SetSize(100, 50)
	d.Show()
	d.filterModelSuggestions()
	if slices.Contains(d.modelSuggestions, "gpt-7-nova") {
		t.Fatalf("probe disabled but suggestions include a probed model: %v", d.modelSuggestions)
	}
	if d.modelSuggestions[0] != "gpt-6-astra" {
		t.Fatalf("static order changed: %v", d.modelSuggestions)
	}
}

// Changing the model drops a selected effort the new model does not accept,
// and keeps one it does.
func TestIssue2388_ModelChangeDropsUnsupportedEffort(t *testing.T) {
	installIssue2388FakeCodex(t)

	d := NewNewDialog()
	d.SetDefaultTool("codex")
	d.SetSize(100, 50)
	d.Show()

	d.reasoningEffort = "hyper"
	d.modelInput.SetValue("gpt-7-nova")
	d.dropUnsupportedReasoningEffort()
	if d.reasoningEffort != "hyper" {
		t.Fatalf("a supported effort was dropped: %q", d.reasoningEffort)
	}

	d.modelInput.SetValue("gpt-5.5")
	d.dropUnsupportedReasoningEffort()
	if d.reasoningEffort != "" {
		t.Fatalf("effort = %q, want it cleared for a model without hyper", d.reasoningEffort)
	}
}
