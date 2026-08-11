package session

import (
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"testing"
)

func TestLoadUserConfig_ParsesOrchestrateToolStrategy(t *testing.T) {
	configDir := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", configDir)
	ClearUserConfigCache()
	t.Cleanup(ClearUserConfigCache)

	path := filepath.Join(configDir, "agent-deck", UserConfigFileName)
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("default_tool = \"codex\"\n\n[orchestrate]\ntool_strategy = \"auto\"\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	cfg, err := LoadUserConfig()
	if err != nil {
		t.Fatalf("LoadUserConfig() error = %v", err)
	}
	field := reflect.ValueOf(*cfg).FieldByName("Orchestrate")
	if !field.IsValid() {
		t.Fatal("UserConfig has no Orchestrate field")
	}
	strategy := field.FieldByName("ToolStrategy")
	if !strategy.IsValid() {
		t.Fatal("Orchestrate settings have no ToolStrategy field")
	}
	if got := strategy.String(); got != "auto" {
		t.Fatalf("ToolStrategy = %q, want auto", got)
	}
}

func TestUserConfig_ResolveOrchestrateToolPolicy_AutoUsesInstalledVisibleTools(t *testing.T) {
	withStubbedProbe(t, []string{"claude", "codex"}, func() {
		cfg := &UserConfig{
			DefaultTool: "codex",
			Orchestrate: OrchestrateSettings{ToolStrategy: "auto"},
		}
		cfg.UI.HiddenTools = []string{"claude"}

		method := reflect.ValueOf(cfg).MethodByName("ResolveOrchestrateToolPolicy")
		if !method.IsValid() {
			t.Fatal("UserConfig has no ResolveOrchestrateToolPolicy method")
		}
		results := method.Call(nil)
		if len(results) != 1 {
			t.Fatalf("ResolveOrchestrateToolPolicy returned %d values, want 1", len(results))
		}
		policy := results[0]
		if got := policy.FieldByName("Strategy").String(); got != "auto" {
			t.Fatalf("Strategy = %q, want auto", got)
		}
		if got := policy.FieldByName("FallbackTool").String(); got != "codex" {
			t.Fatalf("FallbackTool = %q, want codex", got)
		}
		candidateValue := policy.FieldByName("AvailableTools")
		candidates := make([]string, candidateValue.Len())
		for i := range candidates {
			candidates[i] = candidateValue.Index(i).String()
		}
		if want := []string{"codex"}; !slices.Equal(candidates, want) {
			t.Fatalf("AvailableTools = %v, want %v", candidates, want)
		}
	})
}

func TestLoadUserConfig_RejectsInvalidOrchestrateToolStrategy(t *testing.T) {
	configDir := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", configDir)
	ClearUserConfigCache()
	t.Cleanup(ClearUserConfigCache)

	path := filepath.Join(configDir, "agent-deck", UserConfigFileName)
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("[orchestrate]\ntool_strategy = \"sometimes\"\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	_, err := LoadUserConfig()
	if err == nil {
		t.Fatal("LoadUserConfig() error = nil, want invalid tool_strategy error")
	}
	if got := err.Error(); got != `invalid [orchestrate].tool_strategy "sometimes": must be "default" or "auto"` {
		t.Fatalf("LoadUserConfig() error = %q", got)
	}
}
