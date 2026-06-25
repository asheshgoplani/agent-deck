package session

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/BurntSushi/toml"
)

func TestPreAcceptCodexWorkspaceTrust_SeedsHostConfig(t *testing.T) {
	tmpHome := t.TempDir()
	t.Setenv("HOME", tmpHome)
	codexHome := filepath.Join(tmpHome, ".codex")
	if err := os.MkdirAll(codexHome, 0o755); err != nil {
		t.Fatalf("mkdir codex home: %v", err)
	}
	projectDir := filepath.Join(tmpHome, "my-repo")
	if err := os.MkdirAll(projectDir, 0o755); err != nil {
		t.Fatalf("mkdir project: %v", err)
	}

	inst := NewInstance("codex-trust", projectDir)
	inst.Tool = "codex"
	inst.Command = "codex"

	inst.preAcceptCodexWorkspaceTrust()

	configPath := GetCodexConfigPath(codexHome)
	data, err := os.ReadFile(configPath)
	if err != nil {
		t.Fatalf("read %s: %v", configPath, err)
	}
	cfg := map[string]any{}
	if err := toml.Unmarshal(data, &cfg); err != nil {
		t.Fatalf("unmarshal config: %v", err)
	}
	projects, ok := cfg["projects"].(map[string]any)
	if !ok {
		t.Fatalf("projects key missing or wrong type: %T", cfg["projects"])
	}
	absProject, err := filepath.Abs(projectDir)
	if err != nil {
		t.Fatalf("abs project: %v", err)
	}
	entry, ok := projects[absProject].(map[string]any)
	if !ok {
		t.Fatalf("no trust entry for project dir %q in %s", absProject, configPath)
	}
	if entry["trust_level"] != codexTrustLevelTrusted {
		t.Fatalf("trust_level = %v, want %s", entry["trust_level"], codexTrustLevelTrusted)
	}
}

func TestPreAcceptCodexWorkspaceTrust_SkipsSandbox(t *testing.T) {
	tmpHome := t.TempDir()
	t.Setenv("HOME", tmpHome)
	projectDir := filepath.Join(tmpHome, "sandbox-repo")
	if err := os.MkdirAll(projectDir, 0o755); err != nil {
		t.Fatalf("mkdir project: %v", err)
	}

	inst := NewInstance("codex-sandbox", projectDir)
	inst.Tool = "codex"
	inst.Command = "codex"
	inst.Sandbox = &SandboxConfig{Enabled: true, Image: "example/sandbox:latest"}

	inst.preAcceptCodexWorkspaceTrust()

	configPath := GetCodexConfigPath(filepath.Join(tmpHome, ".codex"))
	if _, err := os.Stat(configPath); !os.IsNotExist(err) {
		t.Fatalf("sandbox preAccept should not write host config, stat err=%v", err)
	}
}
