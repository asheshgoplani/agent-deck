package session

import (
	"path/filepath"
	"slices"
	"testing"
)

func TestMergeToolPatternsBuiltinOverrides(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))
	ClearUserConfigCache()
	t.Cleanup(ClearUserConfigCache)

	cfg := &UserConfig{Tools: map[string]ToolDef{
		"pi":     {PromptPatternsExtra: []string{"custom-pi-footer"}},
		"codex":  {BusyPatterns: []string{"custom-busy"}, PromptPatterns: []string{"custom-prompt"}},
		"claude": {BusyPatternsExtra: []string{"custom-claude-busy"}},
	}}
	if err := SaveUserConfig(cfg); err != nil {
		t.Fatal(err)
	}
	ClearUserConfigCache()

	pi := MergeToolPatterns("pi")
	if !slices.Contains(pi.PromptPatterns, "custom-pi-footer") {
		t.Fatalf("pi prompt extras were dropped: %v", pi.PromptPatterns)
	}
	if !slices.Contains(pi.PromptPatterns, `re:(?m)^\s*pi>\s*`) {
		t.Fatal("pi defaults were replaced by extras")
	}
	codex := MergeToolPatterns("codex")
	if !slices.Equal(codex.BusyPatterns, []string{"custom-busy"}) || !slices.Equal(codex.PromptPatterns, []string{"custom-prompt"}) {
		t.Fatalf("codex replacement patterns were dropped: %#v", codex)
	}
	claude := MergeToolPatterns("claude")
	if !slices.Contains(claude.BusyPatterns, "custom-claude-busy") {
		t.Fatalf("claude busy extras were dropped: %v", claude.BusyPatterns)
	}
	if !slices.Contains(claude.BusyPatterns, "ctrl+c to interrupt") {
		t.Fatal("claude defaults were replaced by extras")
	}
	if GetToolDef("pi") != nil || GetToolDef("codex") != nil || GetToolDef("claude") != nil {
		t.Fatal("built-in pattern overrides must not register custom tools")
	}
}

func BenchmarkMergeToolPatternsBuiltinOverrides(b *testing.B) {
	home := b.TempDir()
	b.Setenv("HOME", home)
	b.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))
	ClearUserConfigCache()
	b.Cleanup(ClearUserConfigCache)
	if err := SaveUserConfig(&UserConfig{Tools: map[string]ToolDef{
		"pi": {PromptPatternsExtra: []string{"custom-pi-footer"}},
	}}); err != nil {
		b.Fatal(err)
	}
	for b.Loop() {
		MergeToolPatterns("pi")
	}
}
