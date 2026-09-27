package main

import (
	"os"
	"path/filepath"
	"slices"
	"testing"

	"github.com/asheshgoplani/agent-deck/internal/session"
)

// Issue #2388: `launch -capabilities --json` reports the same model and effort
// lists the local dialogs use, including a probed Codex catalog.

func capabilityTool(t *testing.T, catalog *session.RemoteCreationCatalog, name string) session.RemoteCreationTool {
	t.Helper()
	for _, tool := range catalog.Tools {
		if tool.Name == name {
			return tool
		}
	}
	t.Fatalf("catalog has no %s tool: %+v", name, catalog.Tools)
	return session.RemoteCreationTool{}
}

func TestIssue2388_CapabilitiesReportEfforts(t *testing.T) {
	catalog, err := buildCreationCatalog("issue2388-static")
	if err != nil {
		t.Fatal(err)
	}
	codex := capabilityTool(t, catalog, "codex")
	if !slices.Equal(codex.Models, session.KnownModelIDsForTool("codex")) {
		t.Fatalf("codex models %v", codex.Models)
	}
	if !slices.Equal(codex.ReasoningEfforts, session.LaunchReasoningEffortsForTool("codex")) {
		t.Fatalf("codex efforts %v", codex.ReasoningEfforts)
	}
	if codex.ModelEfforts != nil {
		t.Fatalf("without a probe there is no per-model data, got %v", codex.ModelEfforts)
	}
	if gemini := capabilityTool(t, catalog, "gemini"); len(gemini.ReasoningEfforts) != 0 {
		t.Fatalf("gemini has no effort override, got %v", gemini.ReasoningEfforts)
	}
}

func TestIssue2388_CapabilitiesCarryProbe(t *testing.T) {
	dir := t.TempDir()
	fixture := filepath.Join(dir, "models.json")
	models := `{"models":[{"slug":"gpt-7-nova","visibility":"list","priority":1,"supported_reasoning_levels":[{"effort":"low"},{"effort":"hyper"}]}]}`
	if err := os.WriteFile(fixture, []byte(models), 0o644); err != nil {
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

	catalog, err := buildCreationCatalog("issue2388-probe")
	if err != nil {
		t.Fatal(err)
	}
	codex := capabilityTool(t, catalog, "codex")
	if len(codex.Models) == 0 || codex.Models[0] != "gpt-7-nova" {
		t.Fatalf("codex models = %v, want the probed model first", codex.Models)
	}
	if !slices.Contains(codex.ReasoningEfforts, "hyper") {
		t.Fatalf("codex efforts = %v, want probed hyper", codex.ReasoningEfforts)
	}
	if !slices.Equal(codex.ModelEfforts["gpt-7-nova"], []string{"low", "hyper"}) {
		t.Fatalf("model_efforts = %v", codex.ModelEfforts)
	}
}
