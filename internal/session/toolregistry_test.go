package session

import (
	"reflect"
	"testing"
)

// canonicalBuiltins is the canonical 11, in the precedence order that
// Registry.Match() (and the legacy detectTool() switch) walk.
var canonicalBuiltins = []string{
	"claude", "opencode", "gemini", "codex", "pi",
	"copilot", "crush", "cursor", "hermes", "aider", "shell",
}

func TestRegistry_AllReturnsCanonical11(t *testing.T) {
	all := Init(nil).All()
	if len(all) != len(canonicalBuiltins) {
		t.Fatalf("All() returned %d entries, want %d", len(all), len(canonicalBuiltins))
	}
	got := make([]string, len(all))
	for i, d := range all {
		got[i] = d.Command
	}
	if !reflect.DeepEqual(got, canonicalBuiltins) {
		t.Errorf("All() order = %v, want %v", got, canonicalBuiltins)
	}
}

// TestRegistry_MatchAllBranches covers every legacy detectTool() arm so the
// migration is provably byte-identical for the default (empty-[tools]) config.
func TestRegistry_MatchAllBranches(t *testing.T) {
	r := Init(nil)
	tests := []struct {
		name string
		cmd  string
		want string
	}{
		// claude
		{"claude bare", "claude", "claude"},
		{"claude path+flags", "/usr/local/bin/claude-code --resume", "claude"},
		{"claude uppercase", "Claude", "claude"},
		// opencode + the open-code alias
		{"opencode bare", "opencode", "opencode"},
		{"opencode hyphen alias", "open-code", "opencode"},
		// gemini
		{"gemini bare", "gemini", "gemini"},
		// codex
		{"codex with flags", "/usr/local/bin/codex --yolo", "codex"},
		// pi — whitespace-token match, NOT substring
		{"pi bare", "pi", "pi"},
		{"pi with flags", "pi --profile dev", "pi"},
		{"pi uppercase", "Pi", "pi"},
		{"pi no false match in epic", "epic", "shell"},
		{"pi no false match in tapioca", "tapioca", "shell"},
		// copilot
		{"copilot with flags", "copilot --resume", "copilot"},
		// crush
		{"crush bare", "crush", "crush"},
		// cursor
		{"cursor agent subcommand", "cursor agent", "cursor"},
		// hermes
		{"hermes bare", "hermes", "hermes"},
		// aider has NO detect arm — commands containing "aider" map to shell
		{"aider falls through to shell", "aider", "shell"},
		// shell fallback
		{"unknown -> shell", "vim", "shell"},
		{"empty -> shell", "", "shell"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := r.Match(tt.cmd); got != tt.want {
				t.Errorf("Match(%q) = %q, want %q", tt.cmd, got, tt.want)
			}
		})
	}
}

func TestRegistry_IsBuiltin(t *testing.T) {
	r := Init(nil)
	for _, name := range canonicalBuiltins {
		if !r.IsBuiltin(name) {
			t.Errorf("IsBuiltin(%q) = false, want true", name)
		}
	}
	for _, name := range []string{"my-tool", "mistral", "", "Claude", "vim"} {
		if r.IsBuiltin(name) {
			t.Errorf("IsBuiltin(%q) = true, want false", name)
		}
	}
}

func TestRegistry_CustomTool(t *testing.T) {
	r := Init(map[string]ToolDef{
		"my-tool": {Command: "my-wrapper claude", Icon: "🛠"},
	})

	// Get-able via both the unified Get and the custom-only GetCustom.
	if def := r.Get("my-tool"); def == nil || def.Command != "my-wrapper claude" {
		t.Errorf("Get(\"my-tool\") = %+v, want command %q", def, "my-wrapper claude")
	}
	if def := r.GetCustom("my-tool"); def == nil || def.Icon != "🛠" {
		t.Errorf("GetCustom(\"my-tool\") = %+v, want icon set", def)
	}

	// Appears in CustomNames...
	if names := r.CustomNames(); !reflect.DeepEqual(names, []string{"my-tool"}) {
		t.Errorf("CustomNames() = %v, want [my-tool]", names)
	}
	// ...but is NOT a built-in.
	if r.IsBuiltin("my-tool") {
		t.Error("IsBuiltin(\"my-tool\") = true, want false")
	}
	// Exact-name match resolves to itself (mirrors detectTool's custom-first arm).
	if got := r.Match("my-tool"); got != "my-tool" {
		t.Errorf("Match(\"my-tool\") = %q, want %q", got, "my-tool")
	}
}

// TestRegistry_PrecedenceRejectsShadow pins down precedence rule (a) from
// issue #1258: a [tools.<builtin>] entry is rejected and the built-in wins.
func TestRegistry_PrecedenceRejectsShadow(t *testing.T) {
	r := Init(map[string]ToolDef{
		"claude":  {Command: "my-fork-of-claude"}, // shadows a built-in -> rejected
		"my-tool": {Command: "wrapper"},           // legit custom -> kept
	})

	// The shadow is NOT exposed as a custom tool.
	if def := r.GetCustom("claude"); def != nil {
		t.Errorf("GetCustom(\"claude\") = %+v, want nil (shadow rejected)", def)
	}
	if names := r.CustomNames(); !reflect.DeepEqual(names, []string{"my-tool"}) {
		t.Errorf("CustomNames() = %v, want [my-tool] (claude excluded)", names)
	}

	// claude stays a built-in, and Get/Match return the BUILT-IN, not the fork.
	if !r.IsBuiltin("claude") {
		t.Error("IsBuiltin(\"claude\") = false, want true")
	}
	if def := r.Get("claude"); def == nil || def.Command != "claude" {
		t.Errorf("Get(\"claude\") = %+v, want built-in (command %q)", def, "claude")
	}
	if got := r.Match("claude"); got != "claude" {
		t.Errorf("Match(\"claude\") = %q, want built-in %q", got, "claude")
	}
}
