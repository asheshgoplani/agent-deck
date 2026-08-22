package session

import (
	"reflect"
	"testing"
)

// canonicalBuiltins is the canonical built-in set, in the precedence order that
// Registry.Match() (and the legacy detectTool() switch) walk.
var canonicalBuiltins = []string{
	"claude", "opencode", "gemini", "codex", "pi",
	"copilot", "crush", "cursor", "hermes", "deepseek", "aider", "shell",
}

func TestRegistry_AllReturnsCanonicalBuiltins(t *testing.T) {
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
		{"standalone agent binary", "agent", "cursor"},
		{"standalone agent with flags", "agent --continue", "cursor"},
		// The "agent" arm is a whitespace-token match for exactly this reason:
		// as a substring it would swallow agent-deck's own commands and label
		// every one of them a Cursor session. builtinTools says so in a comment;
		// these cases are what stop a later change from folding "agent" back
		// into detectSubstrings with every test still green.
		{"cursor no false match in agent-deck", "agent-deck", "shell"},
		{"cursor no false match in agent-deck subcommand", "agent-deck session send x", "shell"},
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

// TestRegistry_MatchTokenByPath covers issue #2024: tools detected by token
// (pi, deepseek/dsh, cursor/agent) must match when the token is the basename
// of a path field, while near-misses that share letters or path segments
// must still fall back to shell.
func TestRegistry_MatchTokenByPath(t *testing.T) {
	r := Init(nil)
	tests := []struct {
		name string
		cmd  string
		want string
	}{
		// pi
		{"pi bare", "pi", "pi"},
		{"pi absolute", "/usr/local/bin/pi", "pi"},
		{"pi relative", "./pi", "pi"},
		{"pi path with flags", "/usr/local/bin/pi --profile dev", "pi"},
		{"pi env prefix", "env FOO=1 pi", "pi"},
		{"pi env prefix path", "env FOO=1 /usr/local/bin/pi", "pi"},
		{"pi uppercase path", "/usr/local/bin/Pi", "pi"},
		{"pi double-quoted path with spaces", `"/opt/AI Tools/pi"`, "pi"},
		{"pi single-quoted path with spaces", `'/opt/AI Tools/pi'`, "pi"},
		{"pi backslash-escaped space", `/opt/AI\ Tools/pi`, "pi"},
		{"pi spaced path with trailing flags", `"/opt/AI Tools/pi" --profile dev`, "pi"},
		{"pi spaced path after env assignment", `env PROFILE='team dev' "/opt/AI Tools/pi" --profile dev`, "pi"},
		{"pi near-miss epic", "epic", "shell"},
		{"pi near-miss tapioca", "tapioca", "shell"},
		{"pi path segment not basename", "/home/pi/bin/tool", "shell"},
		{"pi basename with suffix", "/usr/local/bin/pip", "shell"},
		// deepseek / dsh
		{"dsh bare", "dsh", "deepseek"},
		{"dsh with flags", "dsh --model x", "deepseek"},
		{"dsh absolute", "/usr/local/bin/dsh", "deepseek"},
		{"dsh relative", "./dsh", "deepseek"},
		{"dsh path with flags", "./dsh --model x", "deepseek"},
		{"dsh near-miss dshell path", "/usr/bin/dshell", "shell"},
		{"dsh near-miss dshell bare", "dshell", "shell"},
		{"dsh near-miss fdsh", "fdsh", "shell"},
		{"dsh path segment not basename", "/opt/dsh/bin/other", "shell"},
		// cursor / agent
		{"agent bare", "agent", "cursor"},
		{"agent absolute", "/usr/local/bin/agent", "cursor"},
		{"agent relative", "./agent", "cursor"},
		{"agent near-miss agent-deck", "agent-deck", "shell"},
		{"agent path segment not basename", "/home/agent/bin/tool", "shell"},
		// Only the executable field is basename-matched. Paths in argument
		// position, VAR=value assignments, trailing slashes, and suffixed
		// basenames must keep the pre-#2024 behaviour (shell / substring tool).
		{"copilot arg path under pi home", "copilot --cwd /home/pi", "copilot"},
		{"cursor arg path under pi home", "cursor --dir /home/pi", "cursor"},
		{"crush arg path under pi home", "crush /home/pi", "crush"},
		{"hermes arg path under agent home", "hermes --home /opt/agent", "hermes"},
		{"cd pi home then bash", "cd /home/pi && bash", "shell"},
		{"HOME assignment pi path", "HOME=/home/pi bash", "shell"},
		{"VAR assignment pi path", "VAR=/x/pi cmd", "shell"},
		{"ls arg path dsh", "ls /opt/dsh", "shell"},
		{"cat arg path agent", "cat /usr/local/bin/agent", "shell"},
		{"pi trailing slash", "pi/", "shell"},
		{"pi trailing slash path", "/usr/local/bin/pi/", "shell"},
		{"dsh suffixed basename", "dsh.sh", "shell"},
		{"dsh suffixed basename path", "/usr/local/bin/dsh.sh", "shell"},
		{"sudo prefix path", "sudo /usr/local/bin/dsh", "deepseek"},
		{"env assignment then path", "env FOO=1 ./pi", "pi"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := r.Match(tt.cmd); got != tt.want {
				t.Errorf("Match(%q) = %q, want %q", tt.cmd, got, tt.want)
			}
		})
	}
}
