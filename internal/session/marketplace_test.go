package session

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// writeInstalledPlugins writes an installed_plugins.json fixture and returns its
// path. Each entry maps a "<name>@<marketplace>" key to one installation at the
// given installPath with scope "user".
func writeInstalledPlugins(t *testing.T, dir string, entries map[string]string) string {
	t.Helper()
	var b strings.Builder
	b.WriteString(`{"version":1,"plugins":{`)
	first := true
	for key, path := range entries {
		if !first {
			b.WriteString(",")
		}
		first = false
		b.WriteString(`"` + key + `":[{"scope":"user","installPath":"` + path + `","version":"1.0.0"}]`)
	}
	b.WriteString(`}}`)
	installedPath := filepath.Join(dir, "installed_plugins.json")
	if err := os.WriteFile(installedPath, []byte(b.String()), 0o644); err != nil {
		t.Fatalf("write installed_plugins.json: %v", err)
	}
	return installedPath
}

// readEnabledPlugins reads <project>/.claude/settings.json and returns its
// enabledPlugins map (nil if the file or key is absent).
func readEnabledPlugins(t *testing.T, projectPath string) map[string]interface{} {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(projectPath, ".claude", "settings.json"))
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		t.Fatalf("read settings.json: %v", err)
	}
	var settings map[string]interface{}
	if err := json.Unmarshal(data, &settings); err != nil {
		t.Fatalf("parse settings.json: %v", err)
	}
	plugins, _ := settings["enabledPlugins"].(map[string]interface{})
	return plugins
}

func TestMarketplacePluginRef(t *testing.T) {
	cases := []struct {
		entry   string
		wantRef string
		wantOK  bool
	}{
		{"installed/loom", "loom", true},
		{"installed/loom@berg-plugins", "loom@berg-plugins", true},
		{"  installed/loom  ", "loom", true},
		{"installed/", "", false},
		{"berg-store/loom", "", false},
		{"loom", "", false},
		{"", "", false},
	}
	for _, c := range cases {
		ref, ok := marketplacePluginRef(c.entry)
		if ok != c.wantOK || ref != c.wantRef {
			t.Errorf("marketplacePluginRef(%q) = (%q, %v), want (%q, %v)", c.entry, ref, ok, c.wantRef, c.wantOK)
		}
	}
}

func TestResolveMarketplacePluginKeyFrom(t *testing.T) {
	dir := t.TempDir()
	installed := writeInstalledPlugins(t, dir, map[string]string{
		"loom@berg-plugins":      "/install/loom",
		"obsidian@obsidian-pack": "/install/obsidian",
	})

	t.Run("bare name resolves to full key", func(t *testing.T) {
		got, err := resolveMarketplacePluginKeyFrom(installed, "loom")
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if got != "loom@berg-plugins" {
			t.Fatalf("got %q, want loom@berg-plugins", got)
		}
	})

	t.Run("marketplace-qualified", func(t *testing.T) {
		got, err := resolveMarketplacePluginKeyFrom(installed, "obsidian@obsidian-pack")
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if got != "obsidian@obsidian-pack" {
			t.Fatalf("got %q, want obsidian@obsidian-pack", got)
		}
	})

	t.Run("not found", func(t *testing.T) {
		if _, err := resolveMarketplacePluginKeyFrom(installed, "nope"); err == nil {
			t.Fatal("expected error for missing plugin")
		}
	})

	t.Run("missing file", func(t *testing.T) {
		if _, err := resolveMarketplacePluginKeyFrom(filepath.Join(dir, "absent.json"), "loom"); err == nil {
			t.Fatal("expected error for missing installed_plugins.json")
		}
	})
}

func TestResolveMarketplacePluginKeyFrom_Ambiguous(t *testing.T) {
	dir := t.TempDir()
	installed := writeInstalledPlugins(t, dir, map[string]string{
		"shared@market-a": "/install/a",
		"shared@market-b": "/install/b",
	})

	if _, err := resolveMarketplacePluginKeyFrom(installed, "shared"); err == nil {
		t.Fatal("expected ambiguity error for a bare name spanning two marketplaces")
	}

	// Qualifying by marketplace disambiguates.
	got, err := resolveMarketplacePluginKeyFrom(installed, "shared@market-b")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got != "shared@market-b" {
		t.Fatalf("got %q, want shared@market-b", got)
	}
}

// TestLoadout_EnablesMarketplacePlugin drives the full loadout path: a config
// with an "installed/<name>" entry must resolve the full key via
// installed_plugins.json and merge enabledPlugins["<key>"] = true into the
// project's .claude/settings.json.
func TestLoadout_EnablesMarketplacePlugin(t *testing.T) {
	tmpHome := withIsolatedHomeAndConfig(t, `
[groups."work".claude]
plugins = ["installed/widget"]
`)

	pluginsRoot := filepath.Join(tmpHome, ".claude", "plugins")
	if err := os.MkdirAll(pluginsRoot, 0o755); err != nil {
		t.Fatalf("mkdir plugins root: %v", err)
	}
	writeInstalledPlugins(t, pluginsRoot, map[string]string{
		"widget@some-market": "/install/widget",
	})

	project := t.TempDir()
	inst := NewInstanceWithGroupAndTool("s1", project, "work/sub", "claude")

	warnings := ApplyConfiguredLoadout(inst)
	if len(warnings) != 0 {
		t.Fatalf("expected clean enablement, got warnings: %v", warnings)
	}

	plugins := readEnabledPlugins(t, project)
	if v, ok := plugins["widget@some-market"].(bool); !ok || !v {
		t.Fatalf("expected enabledPlugins[widget@some-market]=true, got %#v", plugins)
	}

	// Idempotent: a second apply is a healthy no-op.
	if warnings := ApplyConfiguredLoadout(inst); len(warnings) != 0 {
		t.Fatalf("second apply should be a no-op, got warnings: %v", warnings)
	}
}

// TestLoadout_MarketplacePlugin_MergesNonDestructively asserts the merge
// preserves a pre-existing permissions block and a pre-existing enabledPlugins
// entry while adding the new one.
func TestLoadout_MarketplacePlugin_MergesNonDestructively(t *testing.T) {
	tmpHome := withIsolatedHomeAndConfig(t, `
[groups."work".claude]
plugins = ["installed/widget"]
`)

	pluginsRoot := filepath.Join(tmpHome, ".claude", "plugins")
	if err := os.MkdirAll(pluginsRoot, 0o755); err != nil {
		t.Fatalf("mkdir plugins root: %v", err)
	}
	writeInstalledPlugins(t, pluginsRoot, map[string]string{
		"widget@some-market": "/install/widget",
	})

	project := t.TempDir()
	claudeDir := filepath.Join(project, ".claude")
	if err := os.MkdirAll(claudeDir, 0o755); err != nil {
		t.Fatalf("mkdir .claude: %v", err)
	}
	preexisting := `{
  "permissions": {"allow": ["Bash(ls)"]},
  "enabledPlugins": {"other@market": true},
  "model": "claude-opus-4-8"
}`
	if err := os.WriteFile(filepath.Join(claudeDir, "settings.json"), []byte(preexisting), 0o644); err != nil {
		t.Fatalf("write settings.json: %v", err)
	}

	inst := NewInstanceWithGroupAndTool("s1", project, "work/sub", "claude")
	warnings := ApplyConfiguredLoadout(inst)
	if len(warnings) != 0 {
		t.Fatalf("expected clean merge, got warnings: %v", warnings)
	}

	data, _ := os.ReadFile(filepath.Join(claudeDir, "settings.json"))
	var settings map[string]interface{}
	if err := json.Unmarshal(data, &settings); err != nil {
		t.Fatalf("parse merged settings.json: %v", err)
	}
	if _, ok := settings["permissions"]; !ok {
		t.Fatal("permissions key was dropped by the merge")
	}
	if settings["model"] != "claude-opus-4-8" {
		t.Fatalf("model key not preserved: %#v", settings["model"])
	}
	plugins, _ := settings["enabledPlugins"].(map[string]interface{})
	if v, ok := plugins["other@market"].(bool); !ok || !v {
		t.Fatal("pre-existing enabledPlugins entry was dropped")
	}
	if v, ok := plugins["widget@some-market"].(bool); !ok || !v {
		t.Fatalf("new plugin not enabled: %#v", plugins)
	}
}

// TestLoadout_MarketplacePlugin_RefusesWrongShape asserts a legacy/array-shaped
// enabledPlugins is refused (warning) and left untouched, never clobbered.
func TestLoadout_MarketplacePlugin_RefusesWrongShape(t *testing.T) {
	tmpHome := withIsolatedHomeAndConfig(t, `
[groups."work".claude]
plugins = ["installed/widget"]
`)

	pluginsRoot := filepath.Join(tmpHome, ".claude", "plugins")
	if err := os.MkdirAll(pluginsRoot, 0o755); err != nil {
		t.Fatalf("mkdir plugins root: %v", err)
	}
	writeInstalledPlugins(t, pluginsRoot, map[string]string{
		"widget@some-market": "/install/widget",
	})

	project := t.TempDir()
	claudeDir := filepath.Join(project, ".claude")
	if err := os.MkdirAll(claudeDir, 0o755); err != nil {
		t.Fatalf("mkdir .claude: %v", err)
	}
	wrongShape := `{"enabledPlugins": ["legacy-array-form"]}`
	settingsFile := filepath.Join(claudeDir, "settings.json")
	if err := os.WriteFile(settingsFile, []byte(wrongShape), 0o644); err != nil {
		t.Fatalf("write settings.json: %v", err)
	}

	inst := NewInstanceWithGroupAndTool("s1", project, "work/sub", "claude")
	warnings := ApplyConfiguredLoadout(inst)
	if len(warnings) == 0 {
		t.Fatal("expected a warning for the wrong-shaped enabledPlugins")
	}

	// File must be untouched (still the array form).
	data, _ := os.ReadFile(settingsFile)
	var settings map[string]interface{}
	if err := json.Unmarshal(data, &settings); err != nil {
		t.Fatalf("parse settings.json: %v", err)
	}
	if _, isArray := settings["enabledPlugins"].([]interface{}); !isArray {
		t.Fatalf("wrong-shaped enabledPlugins was clobbered: %#v", settings["enabledPlugins"])
	}
}

func TestResolveProjectSettingsPath(t *testing.T) {
	// A normal absolute project dir resolves to its own .claude/settings.json.
	base := t.TempDir()
	got, err := resolveProjectSettingsPath(base)
	if err != nil {
		t.Fatalf("resolveProjectSettingsPath(%q) errored: %v", base, err)
	}
	want := filepath.Join(filepath.Clean(base), ".claude", "settings.json")
	if got != want {
		t.Errorf("got %q, want %q", got, want)
	}
	// The result must be strictly contained in the project dir.
	if !strings.HasPrefix(got, filepath.Clean(base)+string(os.PathSeparator)) {
		t.Errorf("settings path %q escapes project dir %q", got, base)
	}

	// Fail closed: empty, relative, ".", and leading-traversal project paths.
	rejected := []string{
		"",
		".",
		"relative/project",
		"../escape",
	}
	for _, p := range rejected {
		if got, err := resolveProjectSettingsPath(p); err == nil {
			t.Errorf("expected %q to be rejected (fail-closed), got %q", p, got)
		}
	}

	// An interior ".." that filepath.Clean collapses to a normal absolute path
	// cannot escape its own root, so it is legitimately accepted and stays
	// contained under the cleaned base.
	if got, err := resolveProjectSettingsPath("/abs/with/../traversal"); err != nil {
		t.Errorf("cleanable interior traversal should be accepted, got error: %v", err)
	} else if want := filepath.Join("/abs/traversal", ".claude", "settings.json"); got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

// TestResolveProjectSettingsPath_SymlinkEscape exercises the filesystem-
// containment hardening. A project-local `.claude` SYMLINK pointing outside the
// project must be rejected: os.MkdirAll + the atomicWriteFile temp+rename run
// inside filepath.Dir(settingsPath), so when that dir is a symlink the
// read/temp/write land in the symlink target OUTSIDE the project (the codex-
// verified bypass — atomicWriteFile only protects a symlinked FINAL file, not a
// symlinked PARENT dir). resolveProjectSettingsPath gates all three sinks
// (os.ReadFile / os.MkdirAll / atomicWriteFile) in enableMarketplacePlugin, so a
// rejection here protects READ and WRITE alike. Non-vacuous: the lexical-only
// first-round guard returned the path with no error.
func TestResolveProjectSettingsPath_SymlinkEscape(t *testing.T) {
	project := t.TempDir()
	victim := t.TempDir()
	// A settings.json the attacker hopes to read/clobber, sitting in the victim.
	if err := os.WriteFile(filepath.Join(victim, "settings.json"), []byte(`{"enabledPlugins":{"keep":true}}`), 0o600); err != nil {
		t.Fatalf("seed victim settings: %v", err)
	}

	claude := filepath.Join(project, ".claude")

	// 1. Symlinked .claude parent escaping the project → rejected, and nothing
	//    new is created in the victim dir.
	if err := os.Symlink(victim, claude); err != nil {
		t.Fatalf("symlink .claude -> victim: %v", err)
	}
	if got, err := resolveProjectSettingsPath(project); err == nil {
		t.Errorf("symlinked .claude escaping the project must be rejected; got %q", got)
	}
	before, _ := os.ReadDir(victim)
	if len(before) != 1 { // only the seeded settings.json
		t.Errorf("rejection must not create files in the victim dir; found %d entries", len(before))
	}

	// 2. A symlinked .claude is no-followed even when it points back INSIDE the
	//    project — a .claude symlink is atypical and refused outright.
	if err := os.Remove(claude); err != nil {
		t.Fatalf("rm .claude symlink: %v", err)
	}
	insideReal := filepath.Join(project, "real-claude")
	if err := os.MkdirAll(insideReal, 0o755); err != nil {
		t.Fatalf("mkdir real-claude: %v", err)
	}
	if err := os.Symlink(insideReal, claude); err != nil {
		t.Fatalf("symlink .claude -> inside: %v", err)
	}
	if got, err := resolveProjectSettingsPath(project); err == nil {
		t.Errorf("a symlinked .claude (even inside the project) must be no-followed; got %q", got)
	}

	// 3. A symlinked settings.json under a REAL .claude dir, resolving outside
	//    the project, must be rejected (the os.ReadFile sink would follow it).
	if err := os.Remove(claude); err != nil {
		t.Fatalf("rm .claude symlink: %v", err)
	}
	if err := os.MkdirAll(claude, 0o755); err != nil {
		t.Fatalf("mkdir real .claude: %v", err)
	}
	if err := os.Symlink(filepath.Join(victim, "settings.json"), filepath.Join(claude, "settings.json")); err != nil {
		t.Fatalf("symlink settings.json -> victim: %v", err)
	}
	if got, err := resolveProjectSettingsPath(project); err == nil {
		t.Errorf("a settings.json symlink escaping the project must be rejected; got %q", got)
	}

	// Control: a real .claude dir with a real (or absent) settings.json inside
	// the project is still accepted — the hardening must not over-reject.
	if err := os.Remove(filepath.Join(claude, "settings.json")); err != nil {
		t.Fatalf("rm settings symlink: %v", err)
	}
	got, err := resolveProjectSettingsPath(project)
	if err != nil {
		t.Fatalf("a real in-project .claude must be accepted, got: %v", err)
	}
	if want := filepath.Join(filepath.Clean(project), ".claude", "settings.json"); got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}
