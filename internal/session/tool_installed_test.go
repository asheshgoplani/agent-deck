package session

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
)

// ASK-211: the New Session form marks missing tools with ToolInstalled, which
// probes whatever show_only_installed_tools says.
func TestToolInstalled(t *testing.T) {
	prev := lookPathFn
	t.Cleanup(func() { lookPathFn = prev })
	lookPathFn = func(name string) (string, error) {
		if name == "codex" {
			return "/usr/bin/codex", nil
		}
		return "", errors.New("not found")
	}
	cases := map[string]bool{"": true, "shell": true, "codex": true, "gemini": false}
	for name, want := range cases {
		if got := ToolInstalled(name); got != want {
			t.Errorf("ToolInstalled(%q) = %v, want %v", name, got, want)
		}
	}
}

// ASK-211 review: a built-in started through a configured command (an
// absolute wrapper, a ~ path, a command with arguments) or Claude's own
// local install (~/.claude/local/claude, often only an alias) is installed,
// even when its bare name is not on PATH.
func TestToolInstalled_FollowsConfiguredCommand(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))

	prevLook, prevStat := lookPathFn, statFn
	t.Cleanup(func() { lookPathFn, statFn = prevLook, prevStat })
	// Only npx is on PATH: a configured "npx -y ..." is probed by npx.
	lookPathFn = func(name string) (string, error) {
		if name == "npx" {
			return "/usr/bin/npx", nil
		}
		return "", errors.New("not found")
	}
	statFn = os.Stat

	userConfigCacheMu.Lock()
	origCache := userConfigCache
	userConfigCacheMu.Unlock()
	t.Cleanup(func() {
		userConfigCacheMu.Lock()
		userConfigCache = origCache
		userConfigCacheMu.Unlock()
	})
	setCfg := func(cfg *UserConfig) {
		userConfigCacheMu.Lock()
		userConfigCache = cfg
		userConfigCacheMu.Unlock()
	}

	wrapper := filepath.Join(home, "opt", "claude-wrapper")
	if err := os.MkdirAll(filepath.Dir(wrapper), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(wrapper, []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatal(err)
	}

	setCfg(&UserConfig{Tools: map[string]ToolDef{}, MCPs: map[string]MCPDef{}})
	if ToolInstalled("claude") {
		t.Fatal("claude with nothing on PATH and no config counts as installed")
	}

	cfg := &UserConfig{Tools: map[string]ToolDef{}, MCPs: map[string]MCPDef{}}
	cfg.Claude.Command = wrapper
	cfg.Codex.Command = "~/opt/claude-wrapper"
	cfg.Gemini.Command = "npx -y @google/gemini-cli"
	setCfg(cfg)
	for _, name := range []string{"claude", "codex", "gemini"} {
		if !ToolInstalled(name) {
			t.Errorf("ToolInstalled(%q) = false with a configured command", name)
		}
	}
	cfg.Claude.Command = filepath.Join(home, "opt", "gone")
	if ToolInstalled("claude") {
		t.Fatal("a configured absolute command that does not exist counts as installed")
	}

	local := filepath.Join(home, ".claude", "local", "claude")
	if err := os.MkdirAll(filepath.Dir(local), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(local, []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	cfg.Claude.Command = ""
	if !ToolInstalled("claude") {
		t.Fatal("Claude's own local install (~/.claude/local/claude) does not count")
	}
}

// A configured absolute command with arguments is probed by its program,
// not by the whole line: "/opt/bin/claude --verbose" is installed when
// /opt/bin/claude exists, and missing when it does not. A ~ program with
// arguments is expanded the same way.
func TestToolInstalled_AbsoluteCommandWithArguments(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))

	prevLook, prevStat := lookPathFn, statFn
	t.Cleanup(func() { lookPathFn, statFn = prevLook, prevStat })
	lookPathFn = func(string) (string, error) { return "", errors.New("not found") }
	statFn = os.Stat
	withToolInstalledConfig(t)

	bin := filepath.Join(home, "opt", "bin", "claude")
	writeExecutable(t, bin)

	cfg := &UserConfig{Tools: map[string]ToolDef{}, MCPs: map[string]MCPDef{}}
	cfg.Claude.Command = bin + " --verbose"
	setToolInstalledConfig(cfg)
	if !ToolInstalled("claude") {
		t.Fatalf("ToolInstalled(claude) = false for %q with the program on disk", cfg.Claude.Command)
	}
	cfg.Codex.Command = "~/opt/bin/claude --flag"
	if !ToolInstalled("codex") {
		t.Fatalf("ToolInstalled(codex) = false for %q with the program on disk", cfg.Codex.Command)
	}
	cfg.Claude.Command = filepath.Join(home, "opt", "bin", "gone") + " --verbose"
	if ToolInstalled("claude") {
		t.Fatalf("ToolInstalled(claude) = true for %q, whose program does not exist", cfg.Claude.Command)
	}
}

// The pane prepends the user bin dirs (~/.local/bin, ~/bin) to PATH before
// it runs the tool (spawn_path.go), so a tool installed only there is
// installed even when the TUI's own PATH lacks those dirs.
func TestToolInstalled_UsesSpawnPathDirs(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))
	t.Setenv("PATH", filepath.Join(home, "empty"))

	prevLook, prevStat := lookPathFn, statFn
	t.Cleanup(func() { lookPathFn, statFn = prevLook, prevStat })
	lookPathFn = func(string) (string, error) { return "", errors.New("not found") }
	statFn = os.Stat
	withToolInstalledConfig(t)
	setToolInstalledConfig(&UserConfig{Tools: map[string]ToolDef{}, MCPs: map[string]MCPDef{}})

	if ToolInstalled("claude") || ToolInstalled("codex") {
		t.Fatal("precondition: nothing installed anywhere")
	}
	writeExecutable(t, filepath.Join(home, ".local", "bin", "claude"))
	writeExecutable(t, filepath.Join(home, "bin", "codex"))
	if !ToolInstalled("claude") {
		t.Fatal("claude in ~/.local/bin (a spawn PATH dir) does not count as installed")
	}
	if !ToolInstalled("codex") {
		t.Fatal("codex in ~/bin (a spawn PATH dir) does not count as installed")
	}

	// A configured wrapper with arguments is resolved the same way.
	cfg := &UserConfig{Tools: map[string]ToolDef{}, MCPs: map[string]MCPDef{}}
	cfg.Gemini.Command = "codex --as-gemini"
	setToolInstalledConfig(cfg)
	if !ToolInstalled("gemini") {
		t.Fatal("a configured command whose program is in ~/bin does not count as installed")
	}
	cfg.Gemini.Command = "nowhere-wrapper gemini"
	if ToolInstalled("gemini") {
		t.Fatal("a configured command whose program is nowhere counts as installed")
	}

	// A file there that is not executable is not a tool.
	if err := os.WriteFile(filepath.Join(home, ".local", "bin", "pi"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	if ToolInstalled("pi") {
		t.Fatal("a non-executable file in ~/.local/bin counts as installed")
	}
}

func writeExecutable(t *testing.T, path string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatal(err)
	}
}

func withToolInstalledConfig(t *testing.T) {
	t.Helper()
	userConfigCacheMu.Lock()
	orig := userConfigCache
	userConfigCacheMu.Unlock()
	t.Cleanup(func() { setToolInstalledConfig(orig) })
}

func setToolInstalledConfig(cfg *UserConfig) {
	userConfigCacheMu.Lock()
	userConfigCache = cfg
	userConfigCacheMu.Unlock()
}
