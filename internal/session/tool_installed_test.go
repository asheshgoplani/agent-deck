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
	lookPathFn = func(string) (string, error) { return "", errors.New("not found") }
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
