package session

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/asheshgoplani/agent-deck/internal/agentpaths"
)

// isolateConfigRoots sets HOME + full XDG base dirs under a temp tree and
// clears the user-config / registry caches so GetToolDef/LoadUserConfig see
// only this sandbox. Mirrors production resolvers (agentpaths.EffectiveConfigPath).
func isolateConfigRoots(t *testing.T) (home, xdgConfig, xdgData, xdgCache string) {
	t.Helper()
	home = t.TempDir()
	xdgConfig = filepath.Join(home, "xdg-config")
	xdgData = filepath.Join(home, "xdg-data")
	xdgCache = filepath.Join(home, "xdg-cache")
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", xdgConfig)
	t.Setenv("XDG_DATA_HOME", xdgData)
	t.Setenv("XDG_CACHE_HOME", xdgCache)
	// Unset profile env so data-dir profile resolution cannot leak.
	t.Setenv("AGENTDECK_PROFILE", "")
	t.Cleanup(ClearUserConfigCache)
	ClearUserConfigCache()
	return home, xdgConfig, xdgData, xdgCache
}

func writeConfigAt(t *testing.T, dir, content string) string {
	t.Helper()
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "config.toml")
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func xdgAgentDeckConfigDir(xdgConfig string) string {
	return filepath.Join(xdgConfig, agentpaths.AppDirName)
}

func legacyAgentDeckConfigDir(home string) string {
	return filepath.Join(home, ".agent-deck")
}

// --- path resolution matrix for GetToolDef / resume_flag ---

func TestConfigIsolation_OnlyXDG_GetToolDefResume(t *testing.T) {
	home, xdgConfig, _, _ := isolateConfigRoots(t)
	// Explicitly ensure legacy path does not exist.
	legacy := legacyAgentDeckConfigDir(home)
	if _, err := os.Stat(legacy); err == nil {
		t.Fatalf("legacy dir should not exist yet: %s", legacy)
	}

	writeConfigAt(t, xdgAgentDeckConfigDir(xdgConfig), `
[tools.xdg-only]
command = "xdg-only"
resume_flag = "--resume-xdg"
`)
	ClearUserConfigCache()

	path, err := GetUserConfigPath()
	if err != nil {
		t.Fatalf("GetUserConfigPath: %v", err)
	}
	wantPath := filepath.Join(xdgAgentDeckConfigDir(xdgConfig), "config.toml")
	if path != wantPath {
		t.Fatalf("GetUserConfigPath = %q, want XDG %q", path, wantPath)
	}

	def := GetToolDef("xdg-only")
	if def == nil {
		t.Fatal("GetToolDef(xdg-only) = nil; XDG-only config not loaded")
	}
	if def.ResumeFlag != "--resume-xdg" {
		t.Fatalf("ResumeFlag = %q, want --resume-xdg", def.ResumeFlag)
	}
	if def.Command != "xdg-only" {
		t.Fatalf("Command = %q", def.Command)
	}

	inst := &Instance{
		Tool:             "xdg-only",
		Command:          "xdg-only",
		GenericSessionID: "sid-xdg",
	}
	if !inst.CanRestartGeneric() {
		t.Fatal("CanRestartGeneric false with XDG resume_flag + id")
	}
	cmd := inst.buildGenericCommand("xdg-only")
	if !strings.Contains(cmd, "--resume-xdg") || !strings.Contains(cmd, "sid-xdg") {
		t.Fatalf("buildGenericCommand = %q", cmd)
	}
}

func TestConfigIsolation_OnlyLegacy_GetToolDefResume(t *testing.T) {
	home, xdgConfig, _, _ := isolateConfigRoots(t)
	// XDG config home is set but agent-deck/config.toml is absent → legacy wins.
	if _, err := os.Stat(filepath.Join(xdgAgentDeckConfigDir(xdgConfig), "config.toml")); !os.IsNotExist(err) && err != nil {
		t.Fatalf("stat xdg: %v", err)
	}

	writeConfigAt(t, legacyAgentDeckConfigDir(home), `
[tools.legacy-only]
command = "legacy-only"
resume_flag = "--resume-legacy"
`)
	ClearUserConfigCache()

	path, err := GetUserConfigPath()
	if err != nil {
		t.Fatalf("GetUserConfigPath: %v", err)
	}
	wantPath := filepath.Join(legacyAgentDeckConfigDir(home), "config.toml")
	if path != wantPath {
		t.Fatalf("GetUserConfigPath = %q, want legacy %q", path, wantPath)
	}

	def := GetToolDef("legacy-only")
	if def == nil {
		t.Fatal("GetToolDef(legacy-only) = nil; legacy-only config not loaded")
	}
	if def.ResumeFlag != "--resume-legacy" {
		t.Fatalf("ResumeFlag = %q, want --resume-legacy", def.ResumeFlag)
	}

	inst := &Instance{
		Tool:             "legacy-only",
		Command:          "legacy-only",
		GenericSessionID: "sid-legacy",
	}
	if !inst.CanRestartGeneric() {
		t.Fatal("CanRestartGeneric false")
	}
	cmd := inst.buildGenericCommand("legacy-only")
	if !strings.Contains(cmd, "--resume-legacy") || !strings.Contains(cmd, "sid-legacy") {
		t.Fatalf("buildGenericCommand = %q", cmd)
	}
}

func TestConfigIsolation_OnlyLegacy_XDGUnset(t *testing.T) {
	// When XDG_CONFIG_HOME is unset, ConfigDir falls back to $HOME/.config.
	// Only legacy ~/.agent-deck/config.toml present → should still load tools.
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", "") // empty → fallback to home/.config
	t.Setenv("XDG_DATA_HOME", filepath.Join(home, "xdg-data"))
	t.Setenv("XDG_CACHE_HOME", filepath.Join(home, "xdg-cache"))
	t.Setenv("AGENTDECK_PROFILE", "")
	t.Cleanup(ClearUserConfigCache)
	ClearUserConfigCache()

	writeConfigAt(t, legacyAgentDeckConfigDir(home), `
[tools.legacy-unset-xdg]
command = "legacy-unset-xdg"
resume_flag = "--resume-unset"
`)
	ClearUserConfigCache()

	path, err := GetUserConfigPath()
	if err != nil {
		t.Fatalf("GetUserConfigPath: %v", err)
	}
	wantPath := filepath.Join(legacyAgentDeckConfigDir(home), "config.toml")
	if path != wantPath {
		t.Fatalf("GetUserConfigPath = %q, want legacy %q (XDG empty falls through)", path, wantPath)
	}

	def := GetToolDef("legacy-unset-xdg")
	if def == nil || def.ResumeFlag != "--resume-unset" {
		t.Fatalf("GetToolDef = %+v, want resume_flag=--resume-unset", def)
	}
}

func TestConfigIsolation_BothPresent_XDGWinsResumeFlag(t *testing.T) {
	home, xdgConfig, _, _ := isolateConfigRoots(t)

	writeConfigAt(t, xdgAgentDeckConfigDir(xdgConfig), `
[tools.dual]
command = "dual"
resume_flag = "--from-xdg"
`)
	writeConfigAt(t, legacyAgentDeckConfigDir(home), `
[tools.dual]
command = "dual"
resume_flag = "--from-legacy"
`)
	// Also put a tool only in legacy — must NOT be visible if XDG wins entirely.
	// (Re-write legacy with extra tool after first write.)
	writeConfigAt(t, legacyAgentDeckConfigDir(home), `
[tools.dual]
command = "dual"
resume_flag = "--from-legacy"

[tools.legacy-exclusive]
command = "legacy-exclusive"
resume_flag = "--legacy-only-tool"
`)
	// XDG has dual + an XDG-only tool.
	writeConfigAt(t, xdgAgentDeckConfigDir(xdgConfig), `
[tools.dual]
command = "dual"
resume_flag = "--from-xdg"

[tools.xdg-exclusive]
command = "xdg-exclusive"
resume_flag = "--xdg-only-tool"
`)
	ClearUserConfigCache()

	path, err := GetUserConfigPath()
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(path, filepath.Join("xdg-config", "agent-deck")) {
		t.Fatalf("expected XDG path to win, got %q", path)
	}

	def := GetToolDef("dual")
	if def == nil {
		t.Fatal("GetToolDef(dual) nil")
	}
	if def.ResumeFlag != "--from-xdg" {
		t.Fatalf("when both configs exist, XDG must win: ResumeFlag=%q (not --from-legacy)", def.ResumeFlag)
	}

	if GetToolDef("xdg-exclusive") == nil {
		t.Fatal("XDG-only tool missing")
	}
	if GetToolDef("legacy-exclusive") != nil {
		t.Fatal("legacy-exclusive must not load when XDG config.toml exists (no merge)")
	}

	inst := &Instance{Tool: "dual", Command: "dual", GenericSessionID: "sid"}
	cmd := inst.buildGenericCommand("dual")
	if !strings.Contains(cmd, "--from-xdg") {
		t.Fatalf("buildGenericCommand used wrong flag: %q", cmd)
	}
	if strings.Contains(cmd, "--from-legacy") {
		t.Fatalf("legacy flag leaked: %q", cmd)
	}
}

func TestConfigIsolation_MissingToolsSection(t *testing.T) {
	_, xdgConfig, _, _ := isolateConfigRoots(t)
	writeConfigAt(t, xdgAgentDeckConfigDir(xdgConfig), `
# no [tools.*] at all
[ui]
theme = "dark"
`)
	ClearUserConfigCache()

	cfg, err := LoadUserConfig()
	if err != nil {
		t.Fatalf("LoadUserConfig: %v", err)
	}
	if cfg.Tools == nil {
		t.Fatal("Tools map should be non-nil empty after load")
	}
	if len(cfg.Tools) != 0 {
		t.Fatalf("Tools should be empty, got %v", cfg.Tools)
	}
	if GetToolDef("anything") != nil {
		t.Fatal("GetToolDef should be nil without tools section")
	}

	inst := &Instance{
		Tool:             "missing-tool",
		Command:          "missing-tool",
		GenericSessionID: "sid-orphan",
	}
	if inst.CanRestartGeneric() {
		t.Fatal("CanRestartGeneric must be false when tool not in config")
	}
	cmd := inst.buildGenericCommand("missing-tool")
	if strings.Contains(cmd, "--resume") {
		t.Fatalf("must not inject resume without ToolDef: %q", cmd)
	}
	// GenericSessionID still readable from field even without ToolDef.
	if got := inst.GetGenericSessionID(); got != "sid-orphan" {
		t.Fatalf("GetGenericSessionID = %q (persisted field should still return)", got)
	}
}

func TestConfigIsolation_ResumeFlagEmptyCommand(t *testing.T) {
	_, xdgConfig, _, _ := isolateConfigRoots(t)
	writeConfigAt(t, xdgAgentDeckConfigDir(xdgConfig), `
[tools.foo]
command = ""
resume_flag = "--resume"
`)
	ClearUserConfigCache()

	def := GetToolDef("foo")
	if def == nil {
		t.Fatal("GetToolDef(foo) nil — empty command should still register the tool")
	}
	if def.Command != "" {
		t.Fatalf("Command = %q, want empty", def.Command)
	}
	if def.ResumeFlag != "--resume" {
		t.Fatalf("ResumeFlag = %q", def.ResumeFlag)
	}

	// Instance.Command is what buildGenericCommand uses as baseCommand;
	// ToolDef.Command empty must not block resume when resume_flag is set.
	inst := &Instance{
		Tool:             "foo",
		Command:          "foo-cli", // runtime command may still be set on instance
		GenericSessionID: "sid-empty-cmd",
	}
	if !inst.CanRestartGeneric() {
		t.Fatal("CanRestartGeneric should be true with resume_flag + id even if ToolDef.Command empty")
	}
	cmd := inst.buildGenericCommand("foo-cli")
	if !strings.Contains(cmd, "--resume") || !strings.Contains(cmd, "sid-empty-cmd") {
		t.Fatalf("buildGenericCommand = %q", cmd)
	}
}

func TestConfigIsolation_ToolNamesHyphenUnderscore(t *testing.T) {
	_, xdgConfig, _, _ := isolateConfigRoots(t)
	// Names shaped like local pi/ollama launchers: hyphens and underscores.
	writeConfigAt(t, xdgAgentDeckConfigDir(xdgConfig), `
[tools.oll-mid]
command = "oll-mid"
resume_flag = "--continue"

[tools.local_ornith]
command = "local_ornith"
resume_flag = "-r"

[tools."local_ornith-style"]
command = "local_ornith-style"
resume_flag = "--session"
`)
	ClearUserConfigCache()

	cases := []struct {
		name, flag, sid string
	}{
		{"oll-mid", "--continue", "uuid-oll"},
		{"local_ornith", "-r", "uuid-ornith"},
		{"local_ornith-style", "--session", "uuid-style"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			def := GetToolDef(tc.name)
			if def == nil {
				t.Fatalf("GetToolDef(%q) = nil", tc.name)
			}
			if def.ResumeFlag != tc.flag {
				t.Fatalf("ResumeFlag = %q, want %q", def.ResumeFlag, tc.flag)
			}
			inst := &Instance{
				Tool:             tc.name,
				Command:          tc.name,
				GenericSessionID: tc.sid,
			}
			if !inst.CanRestartGeneric() {
				t.Fatal("CanRestartGeneric false")
			}
			cmd := inst.buildGenericCommand(tc.name)
			if !strings.Contains(cmd, tc.flag) || !strings.Contains(cmd, tc.sid) {
				t.Fatalf("buildGenericCommand = %q, want %s %s", cmd, tc.flag, tc.sid)
			}
		})
	}
}

func TestConfigIsolation_ProfileDoesNotSplitTools(t *testing.T) {
	// [tools.*] lives in the single user config.toml (not per-profile).
	// AGENTDECK_PROFILE must not hide or fork custom tool defs.
	home, xdgConfig, xdgData, _ := isolateConfigRoots(t)

	writeConfigAt(t, xdgAgentDeckConfigDir(xdgConfig), `
[tools.shared-tool]
command = "shared-tool"
resume_flag = "--resume"

[profiles.work.claude]
config_dir = "~/.claude-work"
`)
	// Seed profile data dirs so profile resolution is non-empty.
	for _, p := range []string{"default", "work"} {
		dir := filepath.Join(xdgData, "agent-deck", "profiles", p)
		if err := os.MkdirAll(dir, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	ClearUserConfigCache()

	// default profile
	t.Setenv("AGENTDECK_PROFILE", "default")
	ClearUserConfigCache()
	defDefault := GetToolDef("shared-tool")
	if defDefault == nil || defDefault.ResumeFlag != "--resume" {
		t.Fatalf("profile=default: GetToolDef = %+v", defDefault)
	}

	// work profile — same tools table
	t.Setenv("AGENTDECK_PROFILE", "work")
	ClearUserConfigCache()
	defWork := GetToolDef("shared-tool")
	if defWork == nil || defWork.ResumeFlag != "--resume" {
		t.Fatalf("profile=work: GetToolDef = %+v (tools must be global, not profile-scoped)", defWork)
	}

	// Config path must still be the single XDG config, independent of profile.
	path, err := GetUserConfigPath()
	if err != nil {
		t.Fatal(err)
	}
	want := filepath.Join(xdgAgentDeckConfigDir(xdgConfig), "config.toml")
	if path != want {
		t.Fatalf("GetUserConfigPath under AGENTDECK_PROFILE=work = %q, want %q", path, want)
	}

	// State DB / profile data may differ, but resume command shape uses same ToolDef.
	inst := &Instance{
		Tool:             "shared-tool",
		Command:          "shared-tool",
		GenericSessionID: "sid-profile",
	}
	if !inst.CanRestartGeneric() {
		t.Fatal("CanRestartGeneric under work profile")
	}
	cmd := inst.buildGenericCommand("shared-tool")
	if !strings.Contains(cmd, "--resume") || !strings.Contains(cmd, "sid-profile") {
		t.Fatalf("cmd = %q", cmd)
	}

	// Sanity: HOME isolation still holds (no write under real home).
	_ = home
}

func TestConfigIsolation_NoConfigFile_Defaults(t *testing.T) {
	home, xdgConfig, _, _ := isolateConfigRoots(t)
	// Neither XDG nor legacy config.toml exists.
	ClearUserConfigCache()

	path, err := GetUserConfigPath()
	if err != nil {
		t.Fatalf("GetUserConfigPath: %v", err)
	}
	// When neither exists, EffectiveConfigPath returns the XDG path (preferred default).
	want := filepath.Join(xdgAgentDeckConfigDir(xdgConfig), "config.toml")
	if path != want {
		t.Fatalf("GetUserConfigPath (missing file) = %q, want preferred XDG %q", path, want)
	}

	cfg, err := LoadUserConfig()
	if err != nil {
		t.Fatalf("LoadUserConfig with missing file should return defaults: %v", err)
	}
	if cfg == nil {
		t.Fatal("cfg nil")
	}
	if GetToolDef("shell") != nil {
		// shell is builtin; GetToolDef is custom-only
		t.Fatal("GetToolDef(shell) should be nil (builtin)")
	}
	if len(cfg.Tools) != 0 {
		t.Fatalf("default Tools should be empty, got %v", cfg.Tools)
	}
	_ = home
}

func TestConfigIsolation_RebootResume_XDGOnlyPath(t *testing.T) {
	// End-to-end: XDG-only config + SQLite persist + cold buildGenericCommand.
	_, xdgConfig, _, _ := isolateConfigRoots(t)
	writeConfigAt(t, xdgAgentDeckConfigDir(xdgConfig), `
[tools.reboot-tool]
command = "reboot-tool"
resume_flag = "--resume"
session_id_env = "REBOOT_SESSION_ID"
`)
	ClearUserConfigCache()

	storage := newTestStorage(t)
	inst := NewInstance("reboot-xdg-only", "/tmp")
	inst.Tool = "reboot-tool"
	inst.Command = "reboot-tool"
	inst.GenericSessionID = "cold-sid-123"
	if err := storage.SaveWithGroups([]*Instance{inst}, NewGroupTreeWithGroups([]*Instance{inst}, nil)); err != nil {
		t.Fatal(err)
	}

	loaded, _, err := storage.LoadWithGroups()
	if err != nil {
		t.Fatal(err)
	}
	cold := loaded[0]
	cold.tmuxSession = nil
	if cold.GenericSessionID != "cold-sid-123" {
		t.Fatalf("lost id: %q", cold.GenericSessionID)
	}
	if GetToolDef("reboot-tool") == nil {
		t.Fatal("tool def missing after load")
	}
	if !cold.CanRestartGeneric() {
		t.Fatal("cold restart should work with XDG-only config")
	}
	cmd := cold.buildGenericCommand("reboot-tool")
	if !strings.Contains(cmd, "--resume") || !strings.Contains(cmd, "cold-sid-123") {
		t.Fatalf("post-reboot cmd = %q", cmd)
	}
}
