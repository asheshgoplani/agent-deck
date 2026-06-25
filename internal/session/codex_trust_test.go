package session_test

import (
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"testing"

	"github.com/BurntSushi/toml"
	"github.com/asheshgoplani/agent-deck/internal/docker"
	"github.com/asheshgoplani/agent-deck/internal/session"
)

func TestPreAcceptCodexTrust_CreatesTrustedProjectEntry(t *testing.T) {
	dir := t.TempDir()
	configPath := filepath.Join(dir, "config.toml")
	projectDir := filepath.Join(dir, "my-project")
	if err := os.MkdirAll(projectDir, 0o755); err != nil {
		t.Fatalf("mkdir project: %v", err)
	}

	if err := session.PreAcceptCodexTrust(configPath, projectDir); err != nil {
		t.Fatalf("PreAcceptCodexTrust: %v", err)
	}

	cfg := readCodexTrustConfig(t, configPath)
	projects := cfg["projects"].(map[string]any)
	absProject, err := filepath.Abs(projectDir)
	if err != nil {
		t.Fatalf("abs project: %v", err)
	}
	entry := projects[absProject].(map[string]any)
	if entry["trust_level"] != "trusted" {
		t.Fatalf("trust_level = %v, want trusted", entry["trust_level"])
	}
}

func TestPreAcceptCodexTrust_PreservesExistingKeys(t *testing.T) {
	dir := t.TempDir()
	configPath := filepath.Join(dir, "config.toml")
	original := `model = "gpt-5"

[projects."/other"]
trust_level = "untrusted"
`
	if err := os.WriteFile(configPath, []byte(original), 0o600); err != nil {
		t.Fatalf("write config: %v", err)
	}

	projectDir := filepath.Join(dir, "repo")
	if err := session.PreAcceptCodexTrust(configPath, projectDir); err != nil {
		t.Fatalf("PreAcceptCodexTrust: %v", err)
	}

	cfg := readCodexTrustConfig(t, configPath)
	if cfg["model"] != "gpt-5" {
		t.Fatalf("model = %v, want gpt-5", cfg["model"])
	}
	projects := cfg["projects"].(map[string]any)
	if other, ok := projects["/other"].(map[string]any); !ok || other["trust_level"] != "untrusted" {
		t.Fatalf("existing /other project entry changed: %v", projects["/other"])
	}
	absProject, _ := filepath.Abs(projectDir)
	entry := projects[absProject].(map[string]any)
	if entry["trust_level"] != "trusted" {
		t.Fatalf("new project trust_level = %v", entry["trust_level"])
	}
}

func TestPreAcceptCodexTrust_RejectsMalformedConfig(t *testing.T) {
	dir := t.TempDir()
	configPath := filepath.Join(dir, "config.toml")
	if err := os.WriteFile(configPath, []byte("[[[not-toml"), 0o600); err != nil {
		t.Fatalf("write config: %v", err)
	}
	if err := session.PreAcceptCodexTrust(configPath, dir); err == nil {
		t.Fatal("expected error for malformed config")
	}
}

func TestPreAcceptCodexTrust_SkipsRewriteWhenAlreadyTrusted(t *testing.T) {
	dir := t.TempDir()
	configPath := filepath.Join(dir, "config.toml")
	projectDir := filepath.Join(dir, "repo")
	if err := os.MkdirAll(projectDir, 0o755); err != nil {
		t.Fatalf("mkdir project: %v", err)
	}
	absProject, err := filepath.Abs(projectDir)
	if err != nil {
		t.Fatalf("abs project: %v", err)
	}
	original := fmt.Sprintf(`# keep this comment
model = "gpt-5"

[projects.%q]
trust_level = "trusted"
`, absProject)
	if err := os.WriteFile(configPath, []byte(original), 0o600); err != nil {
		t.Fatalf("write config: %v", err)
	}

	if err := session.PreAcceptCodexTrust(configPath, projectDir); err != nil {
		t.Fatalf("PreAcceptCodexTrust: %v", err)
	}
	got, err := os.ReadFile(configPath)
	if err != nil {
		t.Fatalf("read config: %v", err)
	}
	if string(got) != original {
		t.Fatalf("config rewritten when already trusted:\n--- got ---\n%s\n--- want ---\n%s", got, original)
	}
}

func TestPreAcceptCodexTrust_ConcurrentTrustEntries(t *testing.T) {
	dir := t.TempDir()
	configPath := filepath.Join(dir, "config.toml")
	const n = 8
	var wg sync.WaitGroup
	errs := make(chan error, n)
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			projectDir := filepath.Join(dir, fmt.Sprintf("proj-%d", i))
			if err := os.MkdirAll(projectDir, 0o755); err != nil {
				errs <- err
				return
			}
			errs <- session.PreAcceptCodexTrust(configPath, projectDir)
		}(i)
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatalf("concurrent PreAcceptCodexTrust: %v", err)
		}
	}

	cfg := readCodexTrustConfig(t, configPath)
	projects := cfg["projects"].(map[string]any)
	if len(projects) != n {
		t.Fatalf("projects count = %d, want %d", len(projects), n)
	}
	for i := 0; i < n; i++ {
		projectDir := filepath.Join(dir, fmt.Sprintf("proj-%d", i))
		absProject, _ := filepath.Abs(projectDir)
		entry, ok := projects[absProject].(map[string]any)
		if !ok || entry["trust_level"] != "trusted" {
			t.Fatalf("project %q missing trusted entry: %v", absProject, projects[absProject])
		}
	}
}

func TestApplyMultiRepoCodexContext_CodexOnly(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("HOME", dir)
	codexHome := filepath.Join(dir, ".codex")
	if err := os.MkdirAll(codexHome, 0o755); err != nil {
		t.Fatalf("mkdir codex home: %v", err)
	}
	parentDir := filepath.Join(dir, "multi-repo-worktrees", "feature-x")
	if err := os.MkdirAll(parentDir, 0o755); err != nil {
		t.Fatalf("mkdir parent: %v", err)
	}

	if err := session.ApplyMultiRepoCodexContext("codex", true, parentDir); err != nil {
		t.Fatalf("ApplyMultiRepoCodexContext: %v", err)
	}

	cfg := readCodexTrustConfig(t, session.GetCodexConfigPath(codexHome))
	projects := cfg["projects"].(map[string]any)
	entry := projects[parentDir].(map[string]any)
	if entry["trust_level"] != "trusted" {
		t.Fatalf("trust_level = %v, want trusted", entry["trust_level"])
	}
}

func TestApplyMultiRepoCodexContext_NonCodexIsNoop(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("HOME", dir)
	parentDir := filepath.Join(dir, "parent")
	if err := os.MkdirAll(parentDir, 0o755); err != nil {
		t.Fatalf("mkdir parent: %v", err)
	}

	for _, tool := range []string{"claude", "gemini", ""} {
		if err := session.ApplyMultiRepoCodexContext(tool, true, parentDir); err != nil {
			t.Fatalf("ApplyMultiRepoCodexContext(tool=%q): %v", tool, err)
		}
	}
	configPath := session.GetCodexConfigPath(filepath.Join(dir, ".codex"))
	if _, err := os.Stat(configPath); !os.IsNotExist(err) {
		t.Fatalf("codex config created for non-codex tool, stat err=%v", err)
	}
}

func TestPreAcceptCodexSandboxWorkspaceTrust(t *testing.T) {
	home := t.TempDir()
	if err := session.PreAcceptCodexSandboxWorkspaceTrust(home); err != nil {
		t.Fatalf("PreAcceptCodexSandboxWorkspaceTrust: %v", err)
	}
	configPath := filepath.Join(docker.SandboxDir(home, ".codex"), "config.toml")
	cfg := readCodexTrustConfig(t, configPath)
	projects := cfg["projects"].(map[string]any)
	entry := projects[docker.ContainerWorkDir()].(map[string]any)
	if entry["trust_level"] != "trusted" {
		t.Fatalf("sandbox trust_level = %v, want trusted", entry["trust_level"])
	}
}

func readCodexTrustConfig(t *testing.T, path string) map[string]any {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	cfg := map[string]any{}
	if err := toml.Unmarshal(data, &cfg); err != nil {
		t.Fatalf("unmarshal %s: %v", path, err)
	}
	return cfg
}
