package session

import (
	"path/filepath"
	"strings"
	"testing"
)

// Issue #1577: a custom tool that declares `compatible_with` but has no
// built-in patterns of its own should inherit the compatible preset's
// busy/prompt patterns. Explicit replace fields still override; built-in
// tools are never affected because the fallback only fires when the tool name
// has no defaults of its own.

func busyStrings(t *testing.T, name string) []string {
	t.Helper()
	raw := MergeToolPatterns(name)
	if raw == nil {
		t.Fatalf("MergeToolPatterns(%q) = nil", name)
	}
	return raw.BusyPatterns
}

func TestMergeToolPatterns_CompatibleWithFallback(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))
	ClearUserConfigCache()
	t.Cleanup(ClearUserConfigCache)

	cfg := &UserConfig{
		Tools: map[string]ToolDef{
			// inherits the codewhale preset purely by compatible_with
			"whale": {
				Command:        "codewhale",
				CompatibleWith: "codewhale",
			},
			// explicit busy patterns must win over the inherited preset
			"whale-override": {
				Command:        "codewhale",
				CompatibleWith: "codewhale",
				BusyPatterns:   []string{"CUSTOM-BUSY"},
			},
		},
	}
	if err := SaveUserConfig(cfg); err != nil {
		t.Fatalf("SaveUserConfig: %v", err)
	}
	ClearUserConfigCache()

	// by compatible_with: inherits "waiting for deepseek"
	got := strings.Join(busyStrings(t, "whale"), "|")
	if !strings.Contains(got, "waiting for deepseek") {
		t.Errorf("whale should inherit codewhale busy patterns via compatible_with, got %q", got)
	}

	// explicit override wins
	gotOverride := busyStrings(t, "whale-override")
	if len(gotOverride) != 1 || gotOverride[0] != "CUSTOM-BUSY" {
		t.Errorf("explicit busy_patterns must override the inherited preset, got %v", gotOverride)
	}
}

// TestMergeToolPatterns_BuiltinUnaffectedByFallback proves the fallback cannot
// change any built-in tool's behavior: a name with its own defaults never
// consults compatible_with.
func TestMergeToolPatterns_BuiltinUnaffectedByFallback(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))
	ClearUserConfigCache()
	t.Cleanup(ClearUserConfigCache)

	codex := MergeToolPatterns("codex")
	if codex == nil {
		t.Fatal("codex patterns unexpectedly nil")
	}
	joined := strings.Join(codex.BusyPatterns, "|")
	if strings.Contains(joined, "waiting for deepseek") {
		t.Errorf("built-in codex must not inherit codewhale patterns, got %q", joined)
	}
}
