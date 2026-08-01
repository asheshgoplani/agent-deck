package main

import (
	"flag"
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/asheshgoplani/agent-deck/internal/session"
)

func TestNormalizeArgs(t *testing.T) {
	tests := []struct {
		name     string
		setup    func() *flag.FlagSet // create FlagSet with flags
		args     []string
		expected []string
	}{
		{
			name: "flags already before positional args",
			setup: func() *flag.FlagSet {
				fs := flag.NewFlagSet("test", flag.ContinueOnError)
				fs.Bool("json", false, "")
				return fs
			},
			args:     []string{"--json", "my-title"},
			expected: []string{"--json", "my-title"},
		},
		{
			name: "bool flag after positional arg",
			setup: func() *flag.FlagSet {
				fs := flag.NewFlagSet("test", flag.ContinueOnError)
				fs.Bool("json", false, "")
				return fs
			},
			args:     []string{"my-title", "--json"},
			expected: []string{"--json", "my-title"},
		},
		{
			name: "multiple bool flags after positional arg",
			setup: func() *flag.FlagSet {
				fs := flag.NewFlagSet("test", flag.ContinueOnError)
				fs.Bool("json", false, "")
				fs.Bool("q", false, "")
				return fs
			},
			args:     []string{"my-title", "--json", "-q"},
			expected: []string{"--json", "-q", "my-title"},
		},
		{
			name: "string flag after positional arg",
			setup: func() *flag.FlagSet {
				fs := flag.NewFlagSet("test", flag.ContinueOnError)
				fs.String("message", "", "")
				return fs
			},
			args:     []string{"my-title", "--message", "hello world"},
			expected: []string{"--message", "hello world", "my-title"},
		},
		{
			name: "flag with equals syntax",
			setup: func() *flag.FlagSet {
				fs := flag.NewFlagSet("test", flag.ContinueOnError)
				fs.String("message", "", "")
				return fs
			},
			args:     []string{"my-title", "--message=hello"},
			expected: []string{"--message=hello", "my-title"},
		},
		{
			name: "mixed flags and positional args",
			setup: func() *flag.FlagSet {
				fs := flag.NewFlagSet("test", flag.ContinueOnError)
				fs.Bool("json", false, "")
				fs.Bool("no-wait", false, "")
				return fs
			},
			args:     []string{"my-session", "hello message", "--json", "--no-wait"},
			expected: []string{"--json", "--no-wait", "my-session", "hello message"},
		},
		{
			name: "no flags at all",
			setup: func() *flag.FlagSet {
				fs := flag.NewFlagSet("test", flag.ContinueOnError)
				fs.Bool("json", false, "")
				return fs
			},
			args:     []string{"my-title"},
			expected: []string{"my-title"},
		},
		{
			name: "empty args",
			setup: func() *flag.FlagSet {
				fs := flag.NewFlagSet("test", flag.ContinueOnError)
				fs.Bool("json", false, "")
				return fs
			},
			args:     []string{},
			expected: nil,
		},
		{
			name: "double dash terminator",
			setup: func() *flag.FlagSet {
				fs := flag.NewFlagSet("test", flag.ContinueOnError)
				fs.Bool("json", false, "")
				return fs
			},
			args:     []string{"--", "--json", "title"},
			expected: []string{"--json", "title"},
		},
		{
			name: "session show with title containing special chars",
			setup: func() *flag.FlagSet {
				fs := flag.NewFlagSet("test", flag.ContinueOnError)
				fs.Bool("json", false, "")
				return fs
			},
			args:     []string{"Fix #147: Shift+R Restart Race", "--json"},
			expected: []string{"--json", "Fix #147: Shift+R Restart Race"},
		},
		{
			name: "short flag after positional",
			setup: func() *flag.FlagSet {
				fs := flag.NewFlagSet("test", flag.ContinueOnError)
				fs.Bool("q", false, "")
				return fs
			},
			args:     []string{"my-session", "-q"},
			expected: []string{"-q", "my-session"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			fs := tt.setup()
			result := normalizeArgs(fs, tt.args)
			if !reflect.DeepEqual(result, tt.expected) {
				t.Errorf("normalizeArgs() = %v, want %v", result, tt.expected)
			}
		})
	}
}

// TestNormalizeArgsIntegration verifies that after normalizeArgs + fs.Parse,
// flags are correctly parsed regardless of their position in args.
func TestNormalizeArgsIntegration(t *testing.T) {
	tests := []struct {
		name             string
		args             []string
		expectJSON       bool
		expectQuiet      bool
		expectIdentifier string
	}{
		{
			name:             "flags before identifier",
			args:             []string{"--json", "-q", "my-title"},
			expectJSON:       true,
			expectQuiet:      true,
			expectIdentifier: "my-title",
		},
		{
			name:             "flags after identifier",
			args:             []string{"my-title", "--json", "-q"},
			expectJSON:       true,
			expectQuiet:      true,
			expectIdentifier: "my-title",
		},
		{
			name:             "flags mixed around identifier",
			args:             []string{"--json", "my-title", "-q"},
			expectJSON:       true,
			expectQuiet:      true,
			expectIdentifier: "my-title",
		},
		{
			name:             "only identifier no flags",
			args:             []string{"my-title"},
			expectJSON:       false,
			expectQuiet:      false,
			expectIdentifier: "my-title",
		},
		{
			name:             "title with spaces and special chars",
			args:             []string{"Fix #147: Shift+R Restart Race", "--json"},
			expectJSON:       true,
			expectQuiet:      false,
			expectIdentifier: "Fix #147: Shift+R Restart Race",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			fs := flag.NewFlagSet("test", flag.ContinueOnError)
			jsonOutput := fs.Bool("json", false, "Output as JSON")
			quiet := fs.Bool("q", false, "Quiet mode")

			normalized := normalizeArgs(fs, tt.args)
			if err := fs.Parse(normalized); err != nil {
				t.Fatalf("Parse failed: %v", err)
			}

			identifier := fs.Arg(0)

			if *jsonOutput != tt.expectJSON {
				t.Errorf("json = %v, want %v", *jsonOutput, tt.expectJSON)
			}
			if *quiet != tt.expectQuiet {
				t.Errorf("quiet = %v, want %v", *quiet, tt.expectQuiet)
			}
			if identifier != tt.expectIdentifier {
				t.Errorf("identifier = %q, want %q", identifier, tt.expectIdentifier)
			}
		})
	}
}

func TestReorderArgsForFlagParsing_CmdAndGroup(t *testing.T) {
	tests := []struct {
		name     string
		args     []string
		expected []string
	}{
		{
			name:     "flags already before positional",
			args:     []string{"-c", "claude", "-g", "mygroup", "."},
			expected: []string{"-c", "claude", "-g", "mygroup", "."},
		},
		{
			name:     "path before flags gets moved to end",
			args:     []string{".", "-c", "claude", "-g", "mygroup"},
			expected: []string{"-c", "claude", "-g", "mygroup", "."},
		},
		{
			name:     "mixed flags with --no-parent",
			args:     []string{"-g", "mygroup", "-c", "claude", "--no-parent", "."},
			expected: []string{"-g", "mygroup", "-c", "claude", "--no-parent", "."},
		},
		{
			name:     "equals syntax for -c flag",
			args:     []string{"-c=claude", "-g", "work", "."},
			expected: []string{"-c=claude", "-g", "work", "."},
		},
		{
			name:     "model flag keeps its value",
			args:     []string{"-c", "codex", "--model", "gpt-5.5", "."},
			expected: []string{"-c", "codex", "--model", "gpt-5.5", "."},
		},
		{
			name:     "path before model flag gets moved to end",
			args:     []string{".", "-c", "codex", "--model", "gpt-5.5"},
			expected: []string{"-c", "codex", "--model", "gpt-5.5", "."},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := reorderArgsForFlagParsing(tt.args)
			if !reflect.DeepEqual(result, tt.expected) {
				t.Errorf("reorderArgsForFlagParsing(%v) = %v, want %v", tt.args, result, tt.expected)
			}
		})
	}
}

func TestResolveSessionCommand(t *testing.T) {
	tests := []struct {
		name                      string
		raw                       string
		explicitWrapper           string
		wantTool                  string
		wantWrapper               string
		wantNote                  bool
		wantRawCommand            bool
		wantSubcommandPassthrough bool
	}{
		{
			name:           "plain tool uses tool command",
			raw:            "codex",
			wantTool:       "codex",
			wantWrapper:    "",
			wantNote:       false,
			wantRawCommand: false,
		},
		{
			name:           "tool with args auto-wrapper",
			raw:            "codex --dangerously-bypass-approvals-and-sandbox",
			wantTool:       "codex",
			wantWrapper:    "{command} --dangerously-bypass-approvals-and-sandbox",
			wantNote:       true,
			wantRawCommand: false,
		},
		{
			// A generic shell command is kept raw, but — unlike the
			// subcommand-passthrough cases below — it never went through the
			// claude/codex subcommand check at all, so it must NOT be marked
			// SubcommandPassthrough (Claude review, PR #1821 HIGH #1: that
			// flag is the provenance gate buildShellPassthroughCommand relies
			// on, and it must stay false for anything resolveSessionCommand
			// didn't actually validate).
			name:                      "generic shell command kept raw",
			raw:                       "bash -lc 'echo hi'",
			wantTool:                  "shell",
			wantWrapper:               "",
			wantNote:                  false,
			wantRawCommand:            true,
			wantSubcommandPassthrough: false,
		},
		{
			name:            "explicit wrapper wins",
			raw:             "codex --dangerously-bypass-approvals-and-sandbox",
			explicitWrapper: "{command} --my-wrapper-flag",
			wantTool:        "codex",
			wantWrapper:     "{command} --my-wrapper-flag",
			wantNote:        false,
			wantRawCommand:  false,
		},
		{
			// #1800: "claude remote-control --name X" must NOT become a
			// wrapper suffix — that path let flag injection place
			// --session-id/--dangerously-skip-permissions BEFORE the
			// subcommand, silently demoting it to a positional argument of
			// plain interactive claude. The subcommand-shaped first token
			// routes the whole line through unmodified instead.
			name:                      "claude subcommand runs as-is, no wrapper",
			raw:                       "claude remote-control --name rc-test",
			wantTool:                  "shell",
			wantWrapper:               "",
			wantNote:                  true,
			wantRawCommand:            true,
			wantSubcommandPassthrough: true,
		},
		{
			// Any claude subcommand hits this, not just remote-control —
			// confirms there is no per-subcommand special-casing.
			name:                      "different claude subcommand also runs as-is",
			raw:                       "claude mcp list",
			wantTool:                  "shell",
			wantWrapper:               "",
			wantNote:                  true,
			wantRawCommand:            true,
			wantSubcommandPassthrough: true,
		},
		{
			// Codex bot review (PR #1821 P2): "fork" was missing from
			// codexKnownSubcommands, so agent-deck's flags were still
			// injected ahead of it via the wrapper-suffix path. Pins the
			// fix: a known codex subcommand runs as-is like any other.
			name:                      "codex fork subcommand runs as-is, no wrapper",
			raw:                       "codex fork abc123",
			wantTool:                  "shell",
			wantWrapper:               "",
			wantNote:                  true,
			wantRawCommand:            true,
			wantSubcommandPassthrough: true,
		},
		{
			// Explicit wrapper still wins even for a subcommand-shaped
			// command — the user asked for specific flag placement.
			name:            "explicit wrapper wins over subcommand detection",
			raw:             "claude remote-control --name rc-test",
			explicitWrapper: "{command} --explicit",
			wantTool:        "claude",
			wantWrapper:     "{command} --explicit",
			wantNote:        false,
			wantRawCommand:  false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			tool, command, wrapper, note, isPassthrough, err := resolveSessionCommand(tt.raw, tt.explicitWrapper)
			if err != nil {
				t.Fatalf("resolveSessionCommand returned unexpected error: %v", err)
			}

			if tool != tt.wantTool {
				t.Fatalf("tool = %q, want %q", tool, tt.wantTool)
			}
			if wrapper != tt.wantWrapper {
				t.Fatalf("wrapper = %q, want %q", wrapper, tt.wantWrapper)
			}
			if (note != "") != tt.wantNote {
				t.Fatalf("note present = %v, want %v (note=%q)", note != "", tt.wantNote, note)
			}
			if command == "" {
				t.Fatal("command should not be empty")
			}
			if tt.wantRawCommand && command != tt.raw {
				t.Fatalf("command = %q, want raw %q", command, tt.raw)
			}
			if isPassthrough != tt.wantSubcommandPassthrough {
				t.Fatalf("isSubcommandPassthrough = %v, want %v", isPassthrough, tt.wantSubcommandPassthrough)
			}
		})
	}
}

// TestResolveSessionCommand_RefusesUnparseableExtraArgs pins the REFUSE half
// of #1800's fix: when the extra-args portion of --cmd can't be tokenized
// unambiguously (unterminated quote), resolveSessionCommand must return a
// clear error instead of guessing flag placement.
func TestResolveSessionCommand_RefusesUnparseableExtraArgs(t *testing.T) {
	raw := `claude remote-control --name "unterminated`

	_, _, _, _, _, err := resolveSessionCommand(raw, "")
	if err == nil {
		t.Fatalf("expected an error for unterminated quote in %q, got nil", raw)
	}
}

// TestResolveSessionCommand_PlainClaudeUnaffected is the RISK guard: the
// subcommand-detection branch must never fire for the common, currently
// working case of a plain "-c claude" invocation with no extra args.
func TestResolveSessionCommand_PlainClaudeUnaffected(t *testing.T) {
	tool, command, wrapper, note, _, err := resolveSessionCommand("claude", "")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if tool != "claude" {
		t.Fatalf("tool = %q, want claude", tool)
	}
	if wrapper != "" {
		t.Fatalf("wrapper = %q, want empty for plain claude", wrapper)
	}
	if note != "" {
		t.Fatalf("note = %q, want empty for plain claude", note)
	}
	if command == "" {
		t.Fatal("command should not be empty")
	}
}

// TestResolveSessionCommand_CustomToolSubcommand_UsesWrapperSuffix is
// the regression test for the Codex bot P1 review finding on PR #1821: a
// custom tool configured with a `command` override (e.g.
// [tools.reviewbot].command = "/opt/bin/review-wrapper") must resolve
// through that configured command when invoked with a subcommand-shaped
// extra arg (`-c "reviewbot serve"`) — not the literal, possibly
// non-existent-on-PATH tool name the user typed, which is what the raw text
// happens to spell for a built-in tool but is NOT true for a custom one.
func TestResolveSessionCommand_CustomToolSubcommand_UsesWrapperSuffix(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))
	t.Setenv("XDG_DATA_HOME", filepath.Join(home, ".local", "share"))
	t.Setenv("XDG_CACHE_HOME", filepath.Join(home, ".cache"))

	configDir := filepath.Join(home, ".config", "agent-deck")
	if err := os.MkdirAll(configDir, 0o755); err != nil {
		t.Fatalf("mkdir config dir: %v", err)
	}
	toml := "[tools.reviewbot]\ncommand = \"/opt/bin/review-wrapper\"\n"
	if err := os.WriteFile(filepath.Join(configDir, "config.toml"), []byte(toml), 0o600); err != nil {
		t.Fatalf("write config: %v", err)
	}
	session.ClearUserConfigCache()
	t.Cleanup(session.ClearUserConfigCache)

	// Regression test for the Codex bot P1 review finding on PR #1821 (round
	// 1): an earlier version of this fix routed ANY non-flag-shaped first
	// token through the no-injection passthrough, including for custom
	// tools, and replaced the raw typed tool name with a made-up
	// "toolDef.Command + extra" substitution. The correct behavior needs no
	// special-casing at all: only claude/codex are in the known-subcommand
	// allowlist (resolveSessionCommand's doc explains why), so a custom
	// tool's subcommand-shaped --cmd keeps using the ordinary wrapper-suffix
	// path — which was never actually broken for custom tools, since
	// buildGenericCommand appends a tool's dangerous_flag at the very end of
	// the command rather than injecting it ahead of a wrapper substitution.
	tool, command, wrapper, note, _, err := resolveSessionCommand("reviewbot serve", "")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if tool != "reviewbot" {
		t.Fatalf("tool = %q, want reviewbot (custom tools are never routed through the "+
			"claude/codex-only subcommand passthrough)", tool)
	}
	if command != "/opt/bin/review-wrapper" {
		t.Fatalf("command = %q, want the configured toolDef.Command", command)
	}
	wantWrapper := "{command} serve"
	if wrapper != wantWrapper {
		t.Fatalf("wrapper = %q, want %q (wrapper-suffix path, same as any other extra-args case)", wrapper, wantWrapper)
	}
	if note == "" {
		t.Fatal("expected a note explaining the wrapper-suffix routing")
	}
}

// TestResolveSessionCommand_PositionalPromptNotMisclassifiedAsSubcommand is
// the regression test for the review finding on PR #1821 (finding #1,
// High): an earlier version of this fix treated ANY non-flag-shaped first
// extra-args token as a subcommand, which misfires on an ordinary
// positional prompt — e.g. `-c 'claude "review this repo"'` — whose first
// (and only) token isn't flag-shaped either. That version silently dropped
// --session-id / permission-mode injection for a plain flags-then-prompt
// invocation that had always worked correctly before #1821. The fixed
// behavior only treats a first token as a subcommand when it exactly
// matches the claude/codex known-subcommand allowlist; anything else keeps
// the wrapper-suffix path (flags injected, prompt appended after).
func TestResolveSessionCommand_PositionalPromptNotMisclassifiedAsSubcommand(t *testing.T) {
	tests := []struct {
		name string
		raw  string
	}{
		{"quoted multi-word prompt", `claude "review this repo"`},
		{"unquoted single-word prompt", "claude review"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			tool, _, wrapper, _, _, err := resolveSessionCommand(tt.raw, "")
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if tool != "claude" {
				t.Fatalf("tool = %q, want claude (a positional prompt must not be routed to shell "+
					"passthrough, which would drop --session-id/permission-mode injection)", tool)
			}
			if wrapper == "" {
				t.Fatal("expected the wrapper-suffix path (non-empty wrapper) so flags are still injected " +
					"ahead of the prompt")
			}
		})
	}
}

// TestSplitShellTokens_TrailingBackslashRefused pins the REFUSE contract
// (#1800) for a trailing unescaped backslash: it is ambiguous (a literal
// backslash, or an incomplete escape?) so it must error, not silently
// become a literal token.
func TestSplitShellTokens_TrailingBackslashRefused(t *testing.T) {
	_, err := splitShellTokens(`remote-control \`)
	if err == nil {
		t.Fatal("expected an error for a trailing unescaped backslash, got nil")
	}
}

// TestResolveSessionCommand_FlagThenSubcommand_KnownResidualGap pins the
// currently-accepted scope limitation flagged in review of PR #1821
// (finding #4): resolveSessionCommand only inspects the FIRST extra-args
// token to decide subcommand vs. flag. When a root flag precedes the
// subcommand (e.g. "claude --debug remote-control"), the first token IS
// flag-shaped, so the old wrapper-suffix path still fires and can still
// reproduce #1800's flags-before-subcommand ordering. Flag arity is
// unknowable in general (does "--debug" take a value or not?) without a
// full per-tool grammar, so this is a deliberate, documented trade-off
// (see EVIDENCE.md) rather than a bug to fix here — this test exists so a
// future change to the heuristic has to consciously update this pinned
// expectation instead of silently drifting.
func TestResolveSessionCommand_FlagThenSubcommand_KnownResidualGap(t *testing.T) {
	tool, _, wrapper, _, _, err := resolveSessionCommand("claude --debug remote-control", "")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if tool != "claude" {
		t.Fatalf("tool = %q, want claude (known gap: flag-shaped first token still takes the old "+
			"wrapper-suffix path)", tool)
	}
	if wrapper == "" {
		t.Fatal("expected the wrapper-suffix path to fire for this known-gap case (wrapper should be non-empty)")
	}
}

func TestResolveGroupSelection(t *testing.T) {
	tests := []struct {
		name                  string
		currentGroup          string
		cwdDerivedGroup       string
		parentGroup           string
		explicitGroupProvided bool
		inheritGroup          bool
		want                  string
	}{
		{
			name:                  "explicit group wins over parent",
			currentGroup:          "ard",
			parentGroup:           "conductor",
			explicitGroupProvided: true,
			want:                  "ard",
		},
		{
			name:         "inherit parent when no explicit group and no cwd-derived group",
			currentGroup: "",
			parentGroup:  "conductor",
			want:         "conductor",
		},
		{
			name:         "no explicit group and empty parent",
			currentGroup: "",
			parentGroup:  "",
			want:         "",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := resolveGroupSelection(tt.currentGroup, tt.cwdDerivedGroup, tt.parentGroup, tt.explicitGroupProvided, tt.inheritGroup)
			if got != tt.want {
				t.Fatalf("resolveGroupSelection(%q, %q, %q, %v, %v) = %q, want %q",
					tt.currentGroup, tt.cwdDerivedGroup, tt.parentGroup, tt.explicitGroupProvided, tt.inheritGroup, got, tt.want)
			}
		})
	}
}

func TestShouldInheritParentGroup(t *testing.T) {
	tests := []struct {
		name                  string
		explicitGroupProvided bool
		inheritGroupFlag      bool
		isLinkedWorktree      bool
		want                  bool
		wantProbe             bool // whether the git worktree thunk should be consulted
	}{
		{
			name:                  "explicit -g never auto-inherits, and skips the git probe",
			explicitGroupProvided: true,
			isLinkedWorktree:      true,
			want:                  false,
			wantProbe:             false,
		},
		{
			name:             "--inherit-group inherits without probing git",
			inheritGroupFlag: true,
			isLinkedWorktree: false,
			want:             true,
			wantProbe:        false,
		},
		{
			name:             "worktree child auto-inherits (the fleet default)",
			isLinkedWorktree: true,
			want:             true,
			wantProbe:        true,
		},
		{
			name:             "non-worktree child keeps cwd-derived group (e.g. conductor)",
			isLinkedWorktree: false,
			want:             false,
			wantProbe:        true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			probed := false
			got := shouldInheritParentGroup(tt.explicitGroupProvided, tt.inheritGroupFlag, func() bool {
				probed = true
				return tt.isLinkedWorktree
			})
			if got != tt.want {
				t.Fatalf("shouldInheritParentGroup(explicit=%v, flag=%v, worktree=%v) = %v, want %v",
					tt.explicitGroupProvided, tt.inheritGroupFlag, tt.isLinkedWorktree, got, tt.want)
			}
			if probed != tt.wantProbe {
				t.Fatalf("git worktree probe called = %v, want %v (lazy thunk must not run when steps 1-2 decide)", probed, tt.wantProbe)
			}
		})
	}
}
