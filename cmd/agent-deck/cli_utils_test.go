package main

import (
	"flag"
	"reflect"
	"testing"
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
		name            string
		raw             string
		explicitWrapper string
		wantTool        string
		wantWrapper     string
		wantNote        bool
		wantRawCommand  bool
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
			name:           "generic shell command kept raw",
			raw:            "bash -lc 'echo hi'",
			wantTool:       "shell",
			wantWrapper:    "",
			wantNote:       false,
			wantRawCommand: true,
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
			name:           "claude subcommand runs as-is, no wrapper",
			raw:            "claude remote-control --name rc-test",
			wantTool:       "shell",
			wantWrapper:    "",
			wantNote:       true,
			wantRawCommand: true,
		},
		{
			// Any claude subcommand hits this, not just remote-control —
			// confirms there is no per-subcommand special-casing.
			name:           "different claude subcommand also runs as-is",
			raw:            "claude mcp list",
			wantTool:       "shell",
			wantWrapper:    "",
			wantNote:       true,
			wantRawCommand: true,
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
			tool, command, wrapper, note, err := resolveSessionCommand(tt.raw, tt.explicitWrapper)
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
		})
	}
}

// TestResolveSessionCommand_RefusesUnparseableExtraArgs pins the REFUSE half
// of #1800's fix: when the extra-args portion of --cmd can't be tokenized
// unambiguously (unterminated quote), resolveSessionCommand must return a
// clear error instead of guessing flag placement.
func TestResolveSessionCommand_RefusesUnparseableExtraArgs(t *testing.T) {
	raw := `claude remote-control --name "unterminated`

	_, _, _, _, err := resolveSessionCommand(raw, "")
	if err == nil {
		t.Fatalf("expected an error for unterminated quote in %q, got nil", raw)
	}
}

// TestResolveSessionCommand_PlainClaudeUnaffected is the RISK guard: the
// subcommand-detection branch must never fire for the common, currently
// working case of a plain "-c claude" invocation with no extra args.
func TestResolveSessionCommand_PlainClaudeUnaffected(t *testing.T) {
	tool, command, wrapper, note, err := resolveSessionCommand("claude", "")
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
