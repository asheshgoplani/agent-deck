package session

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// Tests for the declarative per-group/per-conductor skill+mcp loadout
// (ApplyConfiguredLoadout) — attach-only floor semantics over the existing
// skill-source/skill-attach + local .mcp.json machinery. Reuses
// withIsolatedHomeAndConfig (pergroupconfig_nested_test.go) and bumpMtime
// (groupclaude_overrides_test.go).

// setupLoadoutStore creates a skill store with one directory skill (alpha),
// skill source "store". Must run after withIsolatedHomeAndConfig so the
// registry lands in the isolated HOME.
func setupLoadoutStore(t *testing.T, tmpHome string) string {
	t.Helper()
	store := filepath.Join(tmpHome, "store")

	alphaDir := filepath.Join(store, "alpha")
	if err := os.MkdirAll(alphaDir, 0o755); err != nil {
		t.Fatalf("mkdir alpha: %v", err)
	}
	skillMD := "---\nname: alpha\ndescription: test skill\n---\n# alpha\n"
	if err := os.WriteFile(filepath.Join(alphaDir, "SKILL.md"), []byte(skillMD), 0o644); err != nil {
		t.Fatalf("write SKILL.md: %v", err)
	}

	betaDir := filepath.Join(store, "beta")
	if err := os.MkdirAll(betaDir, 0o755); err != nil {
		t.Fatalf("mkdir beta: %v", err)
	}
	betaMD := "---\nname: beta\ndescription: second test skill\n---\n# beta\n"
	if err := os.WriteFile(filepath.Join(betaDir, "SKILL.md"), []byte(betaMD), 0o644); err != nil {
		t.Fatalf("write beta SKILL.md: %v", err)
	}

	if err := AddSkillSource("store", store, "test store"); err != nil {
		t.Fatalf("AddSkillSource: %v", err)
	}
	return store
}

func mustLstatSymlink(t *testing.T, path string) {
	t.Helper()
	info, err := os.Lstat(path)
	if err != nil {
		t.Fatalf("expected materialized skill at %s: %v", path, err)
	}
	if info.Mode()&os.ModeSymlink == 0 {
		t.Fatalf("expected %s to be a symlink, mode=%v", path, info.Mode())
	}
}

func TestLoadout_MaterializesSkillsAndMCPs(t *testing.T) {
	tmpHome := withIsolatedHomeAndConfig(t, `
[mcps.memory]
command = "echo"

[groups."work".claude]
plugins = ["store/alpha"]
mcps = ["memory"]
`)
	store := setupLoadoutStore(t, tmpHome)
	project := t.TempDir()
	inst := NewInstanceWithGroupAndTool("s1", project, "work/sub", "claude")

	warnings := ApplyConfiguredLoadout(inst)
	if len(warnings) != 0 {
		t.Fatalf("expected clean materialization, got warnings: %v", warnings)
	}

	target := filepath.Join(project, ".claude", "skills", "alpha")
	mustLstatSymlink(t, target)
	resolved, err := filepath.EvalSymlinks(target)
	if err != nil {
		t.Fatalf("eval symlink: %v", err)
	}
	wantSource, _ := filepath.EvalSymlinks(filepath.Join(store, "alpha"))
	if resolved != wantSource {
		t.Errorf("symlink resolves to %s, want %s", resolved, wantSource)
	}

	manifestPath := filepath.Join(project, ".agent-deck", "skills.toml")
	data, err := os.ReadFile(manifestPath)
	if err != nil {
		t.Fatalf("manifest missing: %v", err)
	}
	if !strings.Contains(string(data), "alpha") {
		t.Errorf("manifest does not record alpha:\n%s", data)
	}

	mcpData, err := os.ReadFile(filepath.Join(project, ".mcp.json"))
	if err != nil {
		t.Fatalf(".mcp.json missing: %v", err)
	}
	if !strings.Contains(string(mcpData), "memory") {
		t.Errorf(".mcp.json does not carry memory:\n%s", mcpData)
	}
}

func TestLoadout_ReassertIsIdempotent(t *testing.T) {
	tmpHome := withIsolatedHomeAndConfig(t, `
[mcps.memory]
command = "echo"

[groups."work".claude]
plugins = ["store/alpha"]
mcps = ["memory"]
`)
	setupLoadoutStore(t, tmpHome)
	project := t.TempDir()
	inst := NewInstanceWithGroupAndTool("s1", project, "work", "claude")

	if w := ApplyConfiguredLoadout(inst); len(w) != 0 {
		t.Fatalf("first apply: %v", w)
	}
	if w := ApplyConfiguredLoadout(inst); len(w) != 0 {
		t.Fatalf("re-assert must be a silent no-op, got: %v", w)
	}
	mustLstatSymlink(t, filepath.Join(project, ".claude", "skills", "alpha"))
}

func TestLoadout_HealsDeletedTarget(t *testing.T) {
	tmpHome := withIsolatedHomeAndConfig(t, `
[groups."work".claude]
plugins = ["store/alpha"]
`)
	setupLoadoutStore(t, tmpHome)
	project := t.TempDir()
	inst := NewInstanceWithGroupAndTool("s1", project, "work", "claude")

	if w := ApplyConfiguredLoadout(inst); len(w) != 0 {
		t.Fatalf("first apply: %v", w)
	}
	target := filepath.Join(project, ".claude", "skills", "alpha")
	if err := os.Remove(target); err != nil {
		t.Fatalf("remove target: %v", err)
	}

	if w := ApplyConfiguredLoadout(inst); len(w) != 0 {
		t.Fatalf("heal pass must not warn, got: %v", w)
	}
	mustLstatSymlink(t, target)
}

func TestLoadout_NeverClobbersForeignDir(t *testing.T) {
	tmpHome := withIsolatedHomeAndConfig(t, `
[groups."work".claude]
plugins = ["store/alpha"]
`)
	setupLoadoutStore(t, tmpHome)
	project := t.TempDir()

	// A human-placed real directory at the loadout target.
	foreign := filepath.Join(project, ".claude", "skills", "alpha")
	if err := os.MkdirAll(foreign, 0o755); err != nil {
		t.Fatalf("mkdir foreign: %v", err)
	}
	sentinel := filepath.Join(foreign, "human-content.md")
	if err := os.WriteFile(sentinel, []byte("precious"), 0o644); err != nil {
		t.Fatalf("write sentinel: %v", err)
	}

	inst := NewInstanceWithGroupAndTool("s1", project, "work", "claude")
	warnings := ApplyConfiguredLoadout(inst)
	if len(warnings) != 1 || !strings.Contains(warnings[0], "alpha") {
		t.Fatalf("expected one skip warning for alpha, got: %v", warnings)
	}

	// The foreign dir and its content must be untouched.
	if data, err := os.ReadFile(sentinel); err != nil || string(data) != "precious" {
		t.Fatalf("foreign dir content clobbered: %v / %q", err, data)
	}
	info, err := os.Lstat(foreign)
	if err != nil || info.Mode()&os.ModeSymlink != 0 {
		t.Fatalf("foreign dir must stay a real dir: %v %v", info, err)
	}
}

func TestLoadout_MissingEntriesWarnDontFail(t *testing.T) {
	tmpHome := withIsolatedHomeAndConfig(t, `
[groups."work".claude]
plugins = ["store/ghost"]
mcps = ["ghostmcp"]
`)
	setupLoadoutStore(t, tmpHome)
	project := t.TempDir()
	inst := NewInstanceWithGroupAndTool("s1", project, "work", "claude")

	warnings := ApplyConfiguredLoadout(inst)
	if len(warnings) != 2 {
		t.Fatalf("expected 2 warnings (skill + mcp), got: %v", warnings)
	}
	joined := strings.Join(warnings, "\n")
	if !strings.Contains(joined, "ghost") || !strings.Contains(joined, "ghostmcp") {
		t.Errorf("warnings must name the missing entries: %v", warnings)
	}
	if _, err := os.Stat(filepath.Join(project, ".mcp.json")); !os.IsNotExist(err) {
		t.Errorf("no .mcp.json should be written when nothing attaches")
	}
}

func TestLoadout_ConfigRemovalDoesNotDetach(t *testing.T) {
	tmpHome := withIsolatedHomeAndConfig(t, `
[mcps.memory]
command = "echo"

[groups."work".claude]
plugins = ["store/alpha"]
mcps = ["memory"]
`)
	setupLoadoutStore(t, tmpHome)
	project := t.TempDir()
	inst := NewInstanceWithGroupAndTool("s1", project, "work", "claude")

	if w := ApplyConfiguredLoadout(inst); len(w) != 0 {
		t.Fatalf("first apply: %v", w)
	}

	// Remove the loadout from config — the floor is attach-only;
	// subtraction must be a deliberate `skill detach`, never a config edit
	// side effect.
	configPath := filepath.Join(tmpHome, ".agent-deck", "config.toml")
	if err := os.WriteFile(configPath, []byte("[claude]\n"), 0o600); err != nil {
		t.Fatalf("rewrite config: %v", err)
	}
	bumpLoadoutConfigMtime(t, configPath)

	if w := ApplyConfiguredLoadout(inst); len(w) != 0 {
		t.Fatalf("apply after removal: %v", w)
	}
	mustLstatSymlink(t, filepath.Join(project, ".claude", "skills", "alpha"))
	if data, err := os.ReadFile(filepath.Join(project, ".mcp.json")); err != nil || !strings.Contains(string(data), "memory") {
		t.Errorf(".mcp.json must keep memory after config removal: %v\n%s", err, data)
	}
}

func TestLoadout_ConductorUnionsWithGroupFloor(t *testing.T) {
	tmpHome := withIsolatedHomeAndConfig(t, `
[groups."work".claude]
plugins = ["store/alpha"]

[conductors.lilu.claude]
plugins = ["store/beta"]
`)
	setupLoadoutStore(t, tmpHome)
	project := t.TempDir()
	inst := NewInstanceWithGroupAndTool("conductor-lilu", project, "work", "claude")

	if w := ApplyConfiguredLoadout(inst); len(w) != 0 {
		t.Fatalf("apply: %v", w)
	}
	mustLstatSymlink(t, filepath.Join(project, ".claude", "skills", "alpha"))
	mustLstatSymlink(t, filepath.Join(project, ".claude", "skills", "beta"))
}

func TestLoadout_SkipsNonClaudeAndSSH(t *testing.T) {
	tmpHome := withIsolatedHomeAndConfig(t, `
[groups."work".claude]
plugins = ["store/alpha"]
`)
	setupLoadoutStore(t, tmpHome)

	t.Run("non-claude tool", func(t *testing.T) {
		project := t.TempDir()
		inst := NewInstanceWithGroupAndTool("s1", project, "work", "codex")
		if w := ApplyConfiguredLoadout(inst); w != nil {
			t.Fatalf("codex session must be a no-op, got: %v", w)
		}
		if _, err := os.Stat(filepath.Join(project, ".claude")); !os.IsNotExist(err) {
			t.Error("no materialization expected for non-claude tools")
		}
	})

	t.Run("ssh session", func(t *testing.T) {
		project := t.TempDir()
		inst := NewInstanceWithGroupAndTool("s2", project, "work", "claude")
		inst.SSHHost = "user@remote"
		if w := ApplyConfiguredLoadout(inst); w != nil {
			t.Fatalf("ssh session must be a no-op, got: %v", w)
		}
		if _, err := os.Stat(filepath.Join(project, ".claude")); !os.IsNotExist(err) {
			t.Error("no materialization expected for ssh sessions")
		}
	})
}

func TestLoadout_BrokenConfigWarnsInactive(t *testing.T) {
	withIsolatedHomeAndConfig(t, `
[groups."work".claude
plugins = [broken
`)
	project := t.TempDir()
	inst := NewInstanceWithGroupAndTool("s1", project, "work", "claude")

	// Instance construction above already consumed the one-shot parse error
	// (LoadUserConfig caches defaults and only the first caller after an
	// mtime change sees the error). Clear the cache so the loadout's own
	// load observes it, independent of call ordering.
	ClearUserConfigCache()

	warnings := ApplyConfiguredLoadout(inst)
	if len(warnings) != 1 || !strings.Contains(warnings[0], "config.toml error") {
		t.Fatalf("broken config must yield exactly the inactive-loadout warning, got: %v", warnings)
	}
}

// bumpLoadoutConfigMtime advances the config file's mtime so the
// mtime-keyed LoadUserConfig cache treats the rewrite as a fresh load.
func bumpLoadoutConfigMtime(t *testing.T, path string) {
	t.Helper()
	st, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat: %v", err)
	}
	newTime := st.ModTime().Add(2 * time.Second)
	if err := os.Chtimes(path, newTime, newTime); err != nil {
		t.Fatalf("chtimes: %v", err)
	}
}
