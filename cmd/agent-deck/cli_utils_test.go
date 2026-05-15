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
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			tool, command, wrapper, note := resolveSessionCommand(tt.raw, tt.explicitWrapper)

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

func TestResolveGroupSelection(t *testing.T) {
	tests := []struct {
		name                  string
		currentGroup          string
		cwdDerivedGroup       string
		parentGroup           string
		explicitGroupProvided bool
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
			got := resolveGroupSelection(tt.currentGroup, tt.cwdDerivedGroup, tt.parentGroup, tt.explicitGroupProvided)
			if got != tt.want {
				t.Fatalf("resolveGroupSelection(%q, %q, %q, %v) = %q, want %q",
					tt.currentGroup, tt.cwdDerivedGroup, tt.parentGroup, tt.explicitGroupProvided, got, tt.want)
			}
		})
	}
}
