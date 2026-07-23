package session

import (
	"os"
	"path/filepath"
	"reflect"
	"sync"
	"testing"
	"time"

	"github.com/BurntSushi/toml"
)

func TestCodexTUISettingsDecodeAndPreserveExplicitFalse(t *testing.T) {
	withIsolatedHomeAndConfig(t, `
[codex.tui]
status_line = ["model-with-reasoning", "context-used", "git-branch"]
status_line_use_colors = false
`)

	cfg, err := LoadUserConfig()
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Codex.TUI == nil {
		t.Fatal("Codex TUI settings were not decoded")
	}
	want := []string{"model-with-reasoning", "context-used", "git-branch"}
	if cfg.Codex.TUI.StatusLine == nil || !reflect.DeepEqual(*cfg.Codex.TUI.StatusLine, want) {
		t.Fatalf("status_line=%v, want %v", cfg.Codex.TUI.StatusLine, want)
	}
	if cfg.Codex.TUI.StatusLineUseColors == nil || *cfg.Codex.TUI.StatusLineUseColors {
		t.Fatalf("status_line_use_colors=%v, want explicit false", cfg.Codex.TUI.StatusLineUseColors)
	}
}

func TestSaveUserConfigPreservesExplicitEmptyCodexStatusLine(t *testing.T) {
	withIsolatedHomeAndConfig(t, `
[codex.tui]
status_line = []
`)

	cfg, err := LoadUserConfig()
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Codex.TUI == nil || cfg.Codex.TUI.StatusLine == nil {
		t.Fatal("explicit empty status_line was not decoded")
	}
	if err := SaveUserConfig(cfg); err != nil {
		t.Fatal(err)
	}
	ClearUserConfigCache()
	reloaded, err := LoadUserConfig()
	if err != nil {
		t.Fatal(err)
	}
	if reloaded.Codex.TUI == nil || reloaded.Codex.TUI.StatusLine == nil {
		t.Fatal("explicit empty status_line was lost during config save")
	}
	if len(*reloaded.Codex.TUI.StatusLine) != 0 {
		t.Fatalf("status_line=%v, want explicit empty list", *reloaded.Codex.TUI.StatusLine)
	}
}

func TestApplyCodexTUISettingsPreservesUnrelatedConfig(t *testing.T) {
	codexHome := t.TempDir()
	configPath := filepath.Join(codexHome, "config.toml")
	existing := `model = "gpt-5.6-sol"

[tui]
theme = "catppuccin-mocha"

[tui.model_availability_nux]
"gpt-5.6-sol" = 4

[mcp_servers.memory]
command = "memory-server"

[projects."/tmp/work"]
trust_level = "trusted"
`
	if err := os.WriteFile(configPath, []byte(existing), 0o600); err != nil {
		t.Fatal(err)
	}
	useColors := true
	statusLine := []string{"model-with-reasoning", "context-used", "git-branch"}
	settings := &CodexTUISettings{
		StatusLine:          &statusLine,
		StatusLineUseColors: &useColors,
	}

	if err := ApplyCodexTUISettings(codexHome, settings); err != nil {
		t.Fatal(err)
	}

	var got map[string]any
	if _, err := toml.DecodeFile(configPath, &got); err != nil {
		t.Fatal(err)
	}
	if got["model"] != "gpt-5.6-sol" {
		t.Fatalf("model was not preserved: %#v", got["model"])
	}
	tui, ok := got["tui"].(map[string]any)
	if !ok {
		t.Fatalf("tui table missing: %#v", got["tui"])
	}
	if tui["theme"] != "catppuccin-mocha" {
		t.Fatalf("tui.theme was not preserved: %#v", tui)
	}
	if tui["status_line_use_colors"] != true {
		t.Fatalf("status_line_use_colors=%#v", tui["status_line_use_colors"])
	}
	if _, ok := tui["model_availability_nux"]; !ok {
		t.Fatalf("tui.model_availability_nux was not preserved: %#v", tui)
	}
	if _, ok := got["mcp_servers"]; !ok {
		t.Fatalf("mcp_servers were not preserved: %#v", got)
	}
	if _, ok := got["projects"]; !ok {
		t.Fatalf("projects were not preserved: %#v", got)
	}
}

func TestApplyCodexTUISettingsPreservesUnrelatedBytesAndComments(t *testing.T) {
	codexHome := t.TempDir()
	configPath := filepath.Join(codexHome, "config.toml")
	existing := `# keep this header exactly
model="gpt-5.6-sol" # deliberate compact formatting

[tui]
# keep this TUI comment
status_line = [
  "old",
  "layout",
]
theme = "catppuccin-mocha"

[projects."/tmp/work"] # keep this table comment
trust_level="trusted"
`
	if err := os.WriteFile(configPath, []byte(existing), 0o600); err != nil {
		t.Fatal(err)
	}
	statusLine := []string{"model-with-reasoning", "current-dir"}
	if err := ApplyCodexTUISettings(codexHome, &CodexTUISettings{StatusLine: &statusLine}); err != nil {
		t.Fatal(err)
	}

	got, err := os.ReadFile(configPath)
	if err != nil {
		t.Fatal(err)
	}
	want := `# keep this header exactly
model="gpt-5.6-sol" # deliberate compact formatting

[tui]
# keep this TUI comment
status_line = ["model-with-reasoning", "current-dir"]
theme = "catppuccin-mocha"

[projects."/tmp/work"] # keep this table comment
trust_level="trusted"
`
	if string(got) != want {
		t.Fatalf("config bytes changed outside the managed assignment\n--- got ---\n%s\n--- want ---\n%s", got, want)
	}
}

func TestApplyCodexTUISettingsIgnoresManagedLookingMultilineStringContent(t *testing.T) {
	codexHome := t.TempDir()
	configPath := filepath.Join(codexHome, "config.toml")
	existing := `[tui]
note = """
status_line = ["documentation text"]
[projects.fake]
"""
theme = "catppuccin-mocha"
`
	if err := os.WriteFile(configPath, []byte(existing), 0o600); err != nil {
		t.Fatal(err)
	}
	statusLine := []string{"model", "current-dir"}
	if err := ApplyCodexTUISettings(codexHome, &CodexTUISettings{StatusLine: &statusLine}); err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile(configPath)
	if err != nil {
		t.Fatal(err)
	}
	want := existing + `status_line = ["model", "current-dir"]
`
	if string(got) != want {
		t.Fatalf("multiline string content changed\n--- got ---\n%s\n--- want ---\n%s", got, want)
	}
}

func TestApplyCodexTUISettingsSupportsQuotedTUITable(t *testing.T) {
	codexHome := t.TempDir()
	configPath := filepath.Join(codexHome, "config.toml")
	existing := `["tui"]
"status_line" = ["old"]
theme = "catppuccin-mocha"
`
	if err := os.WriteFile(configPath, []byte(existing), 0o600); err != nil {
		t.Fatal(err)
	}
	statusLine := []string{"model", "current-dir"}
	if err := ApplyCodexTUISettings(codexHome, &CodexTUISettings{StatusLine: &statusLine}); err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile(configPath)
	if err != nil {
		t.Fatal(err)
	}
	want := `["tui"]
status_line = ["model", "current-dir"]
theme = "catppuccin-mocha"
`
	if string(got) != want {
		t.Fatalf("quoted TUI table was not edited surgically\n--- got ---\n%s\n--- want ---\n%s", got, want)
	}
}

func TestApplyCodexTUISettingsSupportsDottedRootKeys(t *testing.T) {
	codexHome := t.TempDir()
	configPath := filepath.Join(codexHome, "config.toml")
	existing := `"tui"."status_line" = ["old"]
model = "gpt-5.6-sol"

[projects."/tmp/work"]
trust_level = "trusted"
`
	if err := os.WriteFile(configPath, []byte(existing), 0o600); err != nil {
		t.Fatal(err)
	}
	statusLine := []string{"model", "current-dir"}
	useColors := true
	if err := ApplyCodexTUISettings(codexHome, &CodexTUISettings{
		StatusLine:          &statusLine,
		StatusLineUseColors: &useColors,
	}); err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile(configPath)
	if err != nil {
		t.Fatal(err)
	}
	want := `tui.status_line = ["model", "current-dir"]
model = "gpt-5.6-sol"

tui.status_line_use_colors = true
[projects."/tmp/work"]
trust_level = "trusted"
`
	if string(got) != want {
		t.Fatalf("dotted TUI keys were not edited surgically\n--- got ---\n%s\n--- want ---\n%s", got, want)
	}
}

func TestApplyCodexTUISettingsAddsManagedColorBesideUnmanagedDottedStatusLine(t *testing.T) {
	codexHome := t.TempDir()
	configPath := filepath.Join(codexHome, "config.toml")
	existing := `tui.status_line = ["leave", "unchanged"]
model = "gpt-5.6-sol"
`
	if err := os.WriteFile(configPath, []byte(existing), 0o600); err != nil {
		t.Fatal(err)
	}
	useColors := true
	if err := ApplyCodexTUISettings(codexHome, &CodexTUISettings{StatusLineUseColors: &useColors}); err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile(configPath)
	if err != nil {
		t.Fatal(err)
	}
	want := existing + "tui.status_line_use_colors = true\n"
	if string(got) != want {
		t.Fatalf("unmanaged dotted status line changed\n--- got ---\n%s\n--- want ---\n%s", got, want)
	}
}

func TestApplyCodexTUISettingsHonorsExplicitEmptyStatusLine(t *testing.T) {
	codexHome := t.TempDir()
	statusLine := []string{}
	if err := ApplyCodexTUISettings(codexHome, &CodexTUISettings{StatusLine: &statusLine}); err != nil {
		t.Fatal(err)
	}

	var got struct {
		TUI struct {
			StatusLine []string `toml:"status_line"`
		} `toml:"tui"`
	}
	if _, err := toml.DecodeFile(filepath.Join(codexHome, "config.toml"), &got); err != nil {
		t.Fatal(err)
	}
	if got.TUI.StatusLine == nil || len(got.TUI.StatusLine) != 0 {
		t.Fatalf("status_line=%#v, want explicit empty list", got.TUI.StatusLine)
	}
}

func TestApplyCodexTUISettingsRejectsUnsafeHome(t *testing.T) {
	traversingHome := t.TempDir() + string(os.PathSeparator) + "link" + string(os.PathSeparator) + ".." + string(os.PathSeparator) + "home"
	statusLine := []string{"model"}
	for _, codexHome := range []string{"", ".", string(os.PathSeparator), traversingHome} {
		if err := ApplyCodexTUISettings(codexHome, &CodexTUISettings{StatusLine: &statusLine}); err == nil {
			t.Errorf("ApplyCodexTUISettings(%q) succeeded, want unsafe-home error", codexHome)
		}
	}
}

func TestApplyCodexTUISettingsRejectsHomeSymlinkToRoot(t *testing.T) {
	link := filepath.Join(t.TempDir(), "root")
	if err := os.Symlink(string(os.PathSeparator), link); err != nil {
		t.Fatal(err)
	}
	statusLine := []string{"model"}
	if err := ApplyCodexTUISettings(link, &CodexTUISettings{StatusLine: &statusLine}); err == nil {
		t.Fatal("symlink-to-root Codex home succeeded, want unsafe-home error")
	}
}

func TestCodexConfigLockSerializesAliasPaths(t *testing.T) {
	root := t.TempDir()
	realHome := filepath.Join(root, "real")
	if err := os.Mkdir(realHome, 0o700); err != nil {
		t.Fatal(err)
	}
	aliasHome := filepath.Join(root, "alias")
	if err := os.Symlink(realHome, aliasHome); err != nil {
		t.Fatal(err)
	}
	first, err := acquireCodexConfigLock(filepath.Join(realHome, "config.toml"))
	if err != nil {
		t.Fatal(err)
	}
	acquired := make(chan *codexConfigLock, 1)
	errs := make(chan error, 1)
	go func() {
		lock, lockErr := acquireCodexConfigLock(filepath.Join(aliasHome, "config.toml"))
		if lockErr != nil {
			errs <- lockErr
			return
		}
		acquired <- lock
	}()

	select {
	case lock := <-acquired:
		lock.Release()
		first.Release()
		t.Fatal("alias path acquired a second lock while canonical home lock was held")
	case err := <-errs:
		first.Release()
		t.Fatal(err)
	case <-time.After(50 * time.Millisecond):
	}
	first.Release()
	select {
	case lock := <-acquired:
		lock.Release()
	case err := <-errs:
		t.Fatal(err)
	case <-time.After(2 * time.Second):
		t.Fatal("alias lock did not acquire after canonical lock was released")
	}
}

func TestConcurrentCodexConfigWritersPreserveAllManagedState(t *testing.T) {
	codexHome := t.TempDir()
	configPath := filepath.Join(codexHome, "config.toml")
	cfg := &UserConfig{MCPs: map[string]MCPDef{
		"cat": {Command: "echo", Args: []string{"purr"}},
	}}
	restoreCfg := resetUserConfigCache(t, cfg)
	t.Cleanup(restoreCfg)
	statusLine := []string{"model", "current-dir"}
	useColors := true
	settings := &CodexTUISettings{StatusLine: &statusLine, StatusLineUseColors: &useColors}
	projectDir := filepath.Join(t.TempDir(), "project")

	for iteration := 0; iteration < 20; iteration++ {
		if err := os.WriteFile(configPath, []byte("model = \"gpt-5.6-sol\"\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		start := make(chan struct{})
		errs := make(chan error, 3)
		var writers sync.WaitGroup
		writers.Add(3)
		for _, write := range []func() error{
			func() error { return ApplyCodexTUISettings(codexHome, settings) },
			func() error { return WriteCodexMCPConfig(codexHome, []string{"cat"}) },
			func() error { return PreAcceptCodexTrust(configPath, projectDir) },
		} {
			go func(write func() error) {
				defer writers.Done()
				<-start
				errs <- write()
			}(write)
		}
		close(start)
		writers.Wait()
		close(errs)
		for err := range errs {
			if err != nil {
				t.Fatalf("iteration %d: %v", iteration, err)
			}
		}

		var got map[string]any
		if _, err := toml.DecodeFile(configPath, &got); err != nil {
			t.Fatalf("iteration %d: %v", iteration, err)
		}
		tui, _ := got["tui"].(map[string]any)
		if tui == nil || tui["status_line_use_colors"] != true {
			t.Fatalf("iteration %d: TUI state lost: %#v", iteration, got)
		}
		mcps, _ := got["mcp_servers"].(map[string]any)
		if mcps == nil || mcps["cat"] == nil {
			t.Fatalf("iteration %d: MCP state lost: %#v", iteration, got)
		}
		projects, _ := got["projects"].(map[string]any)
		if projects == nil || projects[projectDir] == nil {
			t.Fatalf("iteration %d: trust state lost: %#v", iteration, got)
		}
	}
}

func TestApplyConfiguredLoadoutCodexTUIWithoutSkillsOrMCPs(t *testing.T) {
	home := withIsolatedHomeAndConfig(t, `
[codex.tui]
status_line = ["model", "current-dir"]

[groups.work.codex]
config_dir = "~/.codex-work"
`)
	t.Setenv("CODEX_HOME", "")
	inst := NewInstanceWithGroupAndTool("codex-tui", "", "work", "codex")

	if warnings := ApplyConfiguredLoadout(inst); len(warnings) != 0 {
		t.Fatalf("warnings=%v", warnings)
	}
	assertCodexStatusLine(t, filepath.Join(home, ".codex-work"), []string{"model", "current-dir"})
}

func TestApplyConfiguredLoadoutCodexTUIUsesCommandOverrideWithoutSkills(t *testing.T) {
	home := withIsolatedHomeAndConfig(t, `
[codex]
command = 'CODEX_HOME="~/.codex-command" codex'

[codex.tui]
status_line = ["model", "current-dir"]
`)
	t.Setenv("CODEX_HOME", "")
	inst := NewInstanceWithGroupAndTool("codex-tui", "", "", "codex")

	if warnings := ApplyConfiguredLoadout(inst); len(warnings) != 0 {
		t.Fatalf("warnings=%v", warnings)
	}
	assertCodexStatusLine(t, filepath.Join(home, ".codex-command"), []string{"model", "current-dir"})
}

func TestApplyConfiguredLoadoutCodexTUIUsesProfileHomeWithoutSkills(t *testing.T) {
	home := withIsolatedHomeAndConfig(t, `
[codex.tui]
status_line = ["model", "current-dir"]

[profiles.work.codex]
config_dir = "~/.codex-profile"
`)
	t.Setenv("CODEX_HOME", "")
	t.Setenv("AGENTDECK_PROFILE", "work")
	ClearUserConfigCache()
	inst := NewInstanceWithGroupAndTool("codex-tui", "", "", "codex")

	if warnings := ApplyConfiguredLoadout(inst); len(warnings) != 0 {
		t.Fatalf("warnings=%v", warnings)
	}
	assertCodexStatusLine(t, filepath.Join(home, ".codex-profile"), []string{"model", "current-dir"})
}

func TestApplyConfiguredLoadoutCodexTUIUsesGlobalHomeWithoutSkills(t *testing.T) {
	home := withIsolatedHomeAndConfig(t, `
[codex]
config_dir = "~/.codex-global"

[codex.tui]
status_line = ["model", "current-dir"]
`)
	t.Setenv("CODEX_HOME", "")
	inst := NewInstanceWithGroupAndTool("codex-tui", "", "", "codex")

	if warnings := ApplyConfiguredLoadout(inst); len(warnings) != 0 {
		t.Fatalf("warnings=%v", warnings)
	}
	assertCodexStatusLine(t, filepath.Join(home, ".codex-global"), []string{"model", "current-dir"})
}

func TestApplyConfiguredLoadoutCodexTUIUsesDefaultHomeWithoutSkills(t *testing.T) {
	home := withIsolatedHomeAndConfig(t, `
[codex.tui]
status_line = ["model", "current-dir"]
`)
	t.Setenv("CODEX_HOME", "")
	inst := NewInstanceWithGroupAndTool("codex-tui", "", "", "codex")

	if warnings := ApplyConfiguredLoadout(inst); len(warnings) != 0 {
		t.Fatalf("warnings=%v", warnings)
	}
	assertCodexStatusLine(t, filepath.Join(home, ".codex"), []string{"model", "current-dir"})
}

func TestSyncGroupCodexPluginsAppliesTUIWithoutPlugins(t *testing.T) {
	home := withIsolatedHomeAndConfig(t, `
[codex.tui]
status_line = ["model", "git-branch"]

[groups.work.codex]
config_dir = "~/.codex-work"
`)

	if err := SyncGroupCodexPlugins("work"); err != nil {
		t.Fatal(err)
	}
	assertCodexStatusLine(t, filepath.Join(home, ".codex-work"), []string{"model", "git-branch"})
}

func TestSyncGroupCodexPluginsIgnoresEmptyTUITableWithoutHome(t *testing.T) {
	withIsolatedHomeAndConfig(t, `
[codex.tui]
`)

	if err := SyncGroupCodexPlugins("work"); err != nil {
		t.Fatalf("empty TUI table should be unmanaged: %v", err)
	}
}

func TestSyncGroupCodexPluginsRejectsUnsafeHomeBeforeCreatingIt(t *testing.T) {
	root := t.TempDir()
	escaped := filepath.Join(root, "escaped")
	unsafeHome := root + string(os.PathSeparator) + "base" + string(os.PathSeparator) + ".." + string(os.PathSeparator) + "escaped"
	withIsolatedHomeAndConfig(t, `
[codex.tui]
status_line = ["model"]

[groups.work.codex]
config_dir = "`+unsafeHome+`"
`)

	if err := SyncGroupCodexPlugins("work"); err == nil {
		t.Fatal("unsafe Codex home sync succeeded")
	}
	if _, err := os.Stat(escaped); !os.IsNotExist(err) {
		t.Fatalf("unsafe Codex home was created before validation: %v", err)
	}
}

func assertCodexStatusLine(t *testing.T, codexHome string, want []string) {
	t.Helper()
	var got struct {
		TUI struct {
			StatusLine []string `toml:"status_line"`
		} `toml:"tui"`
	}
	if _, err := toml.DecodeFile(filepath.Join(codexHome, "config.toml"), &got); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(got.TUI.StatusLine, want) {
		t.Fatalf("status_line=%v, want %v", got.TUI.StatusLine, want)
	}
}
