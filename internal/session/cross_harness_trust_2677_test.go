package session

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

// Issue #2677: a cross-harness switch into Claude must pre-seed folder trust
// for the target's working directory, keyed by its realpath and written to
// the .claude.json the target account actually reads. Without it a fresh,
// never-trusted directory leaves the target stuck at Claude's trust prompt.

func seededCodexToClaudeSource(t *testing.T, claudeAccount bool) (cfg *UserConfig, source *Instance, home, realProject string) {
	t.Helper()
	home = t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_DATA_HOME", filepath.Join(home, "data"))
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, "config"))
	t.Setenv("XDG_CACHE_HOME", filepath.Join(home, "cache"))
	t.Setenv("CLAUDE_CONFIG_DIR", "")
	codexHome := filepath.Join(home, "codex-personal")
	t.Setenv("CODEX_HOME", codexHome)

	// The session lives behind a symlink; Claude keys trust by realpath.
	realProject = filepath.Join(home, "real-project")
	if err := os.MkdirAll(realProject, 0o755); err != nil {
		t.Fatal(err)
	}
	linkProject := filepath.Join(home, "link-project")
	if err := os.Symlink(realProject, linkProject); err != nil {
		t.Fatal(err)
	}

	const sid = "26770000-4444-5555-6666-777777777777"
	rollout := filepath.Join(codexHome, "sessions", "2026", "10", "17", "rollout-20261017-"+sid+".jsonl")
	if err := os.MkdirAll(filepath.Dir(rollout), 0o700); err != nil {
		t.Fatal(err)
	}
	data := `{"type":"session_meta","payload":{"id":"` + sid + `"}}` + "\n" +
		`{"type":"response_item","payload":{"type":"message","role":"user","content":[{"type":"input_text","text":"seed context"}]}}` + "\n"
	if err := os.WriteFile(rollout, []byte(data), 0o600); err != nil {
		t.Fatal(err)
	}

	cfg = &UserConfig{Profiles: map[string]ProfileSettings{}}
	if claudeAccount {
		cfg.Profiles["work"] = ProfileSettings{Claude: ProfileClaudeSettings{ConfigDir: filepath.Join(home, "claude-work")}}
	}
	if err := SaveUserConfig(cfg); err != nil {
		t.Fatal(err)
	}
	ClearUserConfigCache()
	t.Cleanup(ClearUserConfigCache)
	source = &Instance{ID: "codex-source", Tool: "codex", Command: "codex", ProjectPath: linkProject, Title: "source", GroupPath: "group", CodexSessionID: sid}
	return cfg, source, home, realProject
}

func claudeTrustAccepted(t *testing.T, claudeJSON, dir string) bool {
	t.Helper()
	data, err := os.ReadFile(claudeJSON)
	if os.IsNotExist(err) {
		return false
	}
	if err != nil {
		t.Fatal(err)
	}
	var parsed struct {
		Projects map[string]struct {
			HasTrustDialogAccepted bool `json:"hasTrustDialogAccepted"`
		} `json:"projects"`
	}
	if err := json.Unmarshal(data, &parsed); err != nil {
		t.Fatalf("parse %s: %v", claudeJSON, err)
	}
	return parsed.Projects[dir].HasTrustDialogAccepted
}

func runCrossHarnessTrustSwitch(t *testing.T, cfg *UserConfig, source *Instance, target SwitchPreviewTarget) *CrossHarnessSwitchResult {
	t.Helper()
	lifecycle := &executorFakeLifecycle{}
	result, err := ExecuteCrossHarnessSwitch(context.Background(), cfg, source, CrossHarnessSwitchOptions{Target: target}, CrossHarnessSwitchDependencies{
		Store: &executorFakeStore{targets: map[string]*Instance{}}, Lifecycle: lifecycle, Observer: &executorFakeObserver{}, SourceOwnership: executorSourceOwnership(),
	})
	if result == nil || !result.TargetCreated || lifecycle.calls != 1 {
		t.Fatalf("switch did not reach target launch: result=%#v err=%v lifecycle=%d", result, err, lifecycle.calls)
	}
	return result
}

func TestCrossHarnessSwitch_PreSeedsClaudeTrustForTargetRealpath_2677(t *testing.T) {
	cfg, source, home, realProject := seededCodexToClaudeSource(t, false)
	runCrossHarnessTrustSwitch(t, cfg, source, SwitchPreviewTarget{Harness: "claude"})
	if !claudeTrustAccepted(t, filepath.Join(home, ".claude.json"), realProject) {
		t.Fatalf("~/.claude.json has no trust entry for target realpath %q", realProject)
	}
}

func TestCrossHarnessSwitch_PreSeedsClaudeTrustInAccountConfigDir_2677(t *testing.T) {
	cfg, source, home, realProject := seededCodexToClaudeSource(t, true)
	runCrossHarnessTrustSwitch(t, cfg, source, SwitchPreviewTarget{Harness: "claude", Account: "work"})
	if !claudeTrustAccepted(t, filepath.Join(home, "claude-work", ".claude.json"), realProject) {
		t.Fatalf("account .claude.json has no trust entry for target realpath %q", realProject)
	}
	if _, err := os.Stat(filepath.Join(home, ".claude.json")); !os.IsNotExist(err) {
		t.Fatalf("account-bound target must not seed ~/.claude.json (stat err=%v)", err)
	}
}

func TestCrossHarnessSwitch_NonClaudeTargetWritesNoClaudeTrust_2677(t *testing.T) {
	cfg, source := seededCrossHarnessExecutorSource(t)
	home, _ := os.UserHomeDir()
	runCrossHarnessTrustSwitch(t, cfg, source, SwitchPreviewTarget{Harness: "codex", Account: "work"})
	if _, err := os.Stat(filepath.Join(home, ".claude.json")); !os.IsNotExist(err) {
		t.Fatalf("claude->codex switch must not write ~/.claude.json (stat err=%v)", err)
	}
}

// The target keeps the source's GroupPath, so a [groups.<g>.claude].config_dir
// decides which .claude.json the target reads. Seeding ~/.claude.json there
// leaves the target stuck at the trust prompt.
func TestCrossHarnessSwitch_PreSeedsClaudeTrustInGroupConfigDir_2677(t *testing.T) {
	cfg, source, home, realProject := seededCodexToClaudeSource(t, false)
	groupDir := filepath.Join(home, "claude-group")
	cfg.Groups = map[string]GroupSettings{source.GroupPath: {Claude: GroupClaudeSettings{ConfigDir: groupDir}}}
	if err := SaveUserConfig(cfg); err != nil {
		t.Fatal(err)
	}
	ClearUserConfigCache()
	runCrossHarnessTrustSwitch(t, cfg, source, SwitchPreviewTarget{Harness: "claude"})
	if !claudeTrustAccepted(t, filepath.Join(groupDir, ".claude.json"), realProject) {
		t.Fatalf("group config dir .claude.json has no trust entry for target realpath %q", realProject)
	}
	if _, err := os.Stat(filepath.Join(home, ".claude.json")); !os.IsNotExist(err) {
		t.Fatalf("group-routed target must not seed ~/.claude.json (stat err=%v)", err)
	}
}

func TestCrossHarnessSwitch_PreSeedsClaudeTrustInGlobalConfigDir_2677(t *testing.T) {
	cfg, source, home, realProject := seededCodexToClaudeSource(t, false)
	globalDir := filepath.Join(home, "claude-global")
	cfg.Claude.ConfigDir = globalDir
	if err := SaveUserConfig(cfg); err != nil {
		t.Fatal(err)
	}
	ClearUserConfigCache()
	runCrossHarnessTrustSwitch(t, cfg, source, SwitchPreviewTarget{Harness: "claude"})
	if !claudeTrustAccepted(t, filepath.Join(globalDir, ".claude.json"), realProject) {
		t.Fatalf("[claude].config_dir .claude.json has no trust entry for target realpath %q", realProject)
	}
	if _, err := os.Stat(filepath.Join(home, ".claude.json")); !os.IsNotExist(err) {
		t.Fatalf("globally routed target must not seed ~/.claude.json (stat err=%v)", err)
	}
}
