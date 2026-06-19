package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/asheshgoplani/agent-deck/internal/session"
	"github.com/asheshgoplani/agent-deck/internal/ui"
)

func TestTmuxAvailable(t *testing.T) {
	_, err := exec.LookPath("tmux")
	if err != nil {
		t.Skip("tmux not available - skipping test")
	}
}

func TestHomeInit(t *testing.T) {
	home := ui.NewHome()
	if home == nil {
		t.Fatal("NewHome() returned nil")
	}
}

func TestHomeView(t *testing.T) {
	home := ui.NewHome()
	view := home.View()
	if view == "" {
		t.Error("View() returned empty string")
	}
}

// TestNestedSessionAllowsCLICommands verifies that CLI subcommands are NOT
// blocked inside managed sessions (fix for #130). Only the interactive TUI
// (no-args) should be blocked.
func TestNestedSessionAllowsCLICommands(t *testing.T) {
	// GetCurrentSessionID returns "" when not in tmux
	t.Run("not_in_tmux", func(t *testing.T) {
		orig := os.Getenv("TMUX")
		os.Unsetenv("TMUX")
		defer os.Setenv("TMUX", orig)

		id := GetCurrentSessionID()
		if id != "" {
			t.Errorf("expected empty session ID outside tmux, got %q", id)
		}
		if isNestedSession() {
			t.Error("isNestedSession() should return false outside tmux")
		}
	})

	// Non-agentdeck tmux session should not be detected as nested
	t.Run("non_agentdeck_tmux", func(t *testing.T) {
		orig := os.Getenv("TMUX")
		os.Setenv("TMUX", "/tmp/tmux-501/default,12345,0")
		defer os.Setenv("TMUX", orig)

		// GetCurrentSessionID shells out to tmux, so if we're not actually
		// in that session it will either fail or return the real session name.
		// The key invariant is: a non-agentdeck session name returns "".
		// We verify this by checking the helper logic directly.
		id := GetCurrentSessionID()
		// In CI/test, either tmux isn't running or we're not in an agentdeck session
		if id != "" {
			t.Logf("got session ID %q (test running inside tmux?)", id)
		}
	})

	// Verify the control flow: subcommands are dispatched before nested check.
	// extractProfileFlag + subcommand dispatch means any args[0] that matches
	// a known command will be handled and return before isNestedSession() runs.
	t.Run("subcommands_dispatched_before_nested_check", func(t *testing.T) {
		// These are all the subcommands that should work inside nested sessions
		subcommands := []string{
			"add", "list", "ls", "remove", "rm", "status",
			"session", "mcp", "skill", "group", "try", "worktree", "wt",
			"profile", "update", "mcp-proxy", "web", "uninstall", "migrate-paths", "hooks", "codex-hooks", "codex-notify", "gemini-hooks", "cursor-hooks",
			"version", "--version", "-v",
			"help", "--help", "-h",
		}
		for _, cmd := range subcommands {
			_, args := extractProfileFlag([]string{cmd})
			if len(args) == 0 {
				t.Errorf("extractProfileFlag consumed subcommand %q, leaving no args", cmd)
			}
			if args[0] != cmd {
				t.Errorf("expected args[0]=%q after extractProfileFlag, got %q", cmd, args[0])
			}
		}
	})

	// Profile flag + subcommand should also pass through
	t.Run("profile_flag_with_subcommand", func(t *testing.T) {
		_, args := extractProfileFlag([]string{"-p", "work", "add", "/tmp"})
		if len(args) == 0 || args[0] != "add" {
			t.Errorf("expected args[0]='add' after profile extraction, got %v", args)
		}
	})

	// No args (TUI mode) with profile flag should leave empty args
	t.Run("profile_flag_only_triggers_tui_block", func(t *testing.T) {
		_, args := extractProfileFlag([]string{"-p", "work"})
		if len(args) != 0 {
			t.Errorf("expected empty args for TUI mode with profile flag, got %v", args)
		}
	})
}

// TestOuterTmuxGuard verifies the generic-tmux TUI guard added for issue #560.
// When a user runs the interactive TUI inside a non-agentdeck tmux session,
// detach semantics get surprising (Ctrl+Q returns to the outer tmux). The
// guard warns and exits unless the user opts in via AGENT_DECK_ALLOW_OUTER_TMUX=1.
func TestOuterTmuxGuard(t *testing.T) {
	// Setup: snapshot env, restore on exit
	fakeBin := t.TempDir()
	fakeTmux := filepath.Join(fakeBin, "tmux")
	if err := os.WriteFile(fakeTmux, []byte("#!/bin/sh\nexit 1\n"), 0o755); err != nil {
		t.Fatalf("write fake tmux: %v", err)
	}
	t.Setenv("PATH", fakeBin+string(os.PathListSeparator)+os.Getenv("PATH"))

	origTmux := os.Getenv("TMUX")
	origOptIn := os.Getenv("AGENT_DECK_ALLOW_OUTER_TMUX")
	t.Cleanup(func() {
		if origTmux == "" {
			os.Unsetenv("TMUX")
		} else {
			os.Setenv("TMUX", origTmux)
		}
		if origOptIn == "" {
			os.Unsetenv("AGENT_DECK_ALLOW_OUTER_TMUX")
		} else {
			os.Setenv("AGENT_DECK_ALLOW_OUTER_TMUX", origOptIn)
		}
	})

	t.Run("outer_tmux_no_optin_blocks", func(t *testing.T) {
		os.Setenv("TMUX", "/tmp/tmux-501/default,12345,0")
		os.Unsetenv("AGENT_DECK_ALLOW_OUTER_TMUX")
		if !isOuterTmuxWithoutOptIn() {
			t.Error("expected guard to fire when TMUX set and no opt-in")
		}
	})

	t.Run("outer_tmux_with_optin_passes", func(t *testing.T) {
		os.Setenv("TMUX", "/tmp/tmux-501/default,12345,0")
		os.Setenv("AGENT_DECK_ALLOW_OUTER_TMUX", "1")
		if isOuterTmuxWithoutOptIn() {
			t.Error("expected guard NOT to fire when opt-in env is set")
		}
	})

	t.Run("no_tmux_passes", func(t *testing.T) {
		os.Unsetenv("TMUX")
		os.Unsetenv("AGENT_DECK_ALLOW_OUTER_TMUX")
		if isOuterTmuxWithoutOptIn() {
			t.Error("expected guard NOT to fire when TMUX is unset")
		}
	})

	t.Run("optin_non_1_value_still_blocks", func(t *testing.T) {
		// Only "1" is the accepted opt-in value — defensively narrow so typos
		// like "true"/"yes" don't silently bypass the guard.
		os.Setenv("TMUX", "/tmp/tmux-501/default,12345,0")
		os.Setenv("AGENT_DECK_ALLOW_OUTER_TMUX", "true")
		if !isOuterTmuxWithoutOptIn() {
			t.Error("expected guard to fire when opt-in is not exactly \"1\"")
		}
	})
}

func TestExtractGroupFlag(t *testing.T) {
	tests := []struct {
		name          string
		args          []string
		wantGroup     string
		wantRemaining []string
	}{
		{
			name:          "no flag",
			args:          []string{"list"},
			wantGroup:     "",
			wantRemaining: []string{"list"},
		},
		{
			name:          "--group=work equals form",
			args:          []string{"--group=work"},
			wantGroup:     "work",
			wantRemaining: nil,
		},
		{
			name:          "--group work space form",
			args:          []string{"--group", "work"},
			wantGroup:     "work",
			wantRemaining: nil,
		},
		{
			name:          "-g work short form",
			args:          []string{"-g", "work"},
			wantGroup:     "work",
			wantRemaining: nil,
		},
		{
			name:          "-g=work short equals form",
			args:          []string{"-g=work"},
			wantGroup:     "work",
			wantRemaining: nil,
		},
		{
			name:          "combined with -p",
			args:          []string{"-p", "myprofile", "-g", "work"},
			wantGroup:     "work",
			wantRemaining: []string{"-p", "myprofile"},
		},
		{
			name:          "subgroup path",
			args:          []string{"--group", "clients/acme"},
			wantGroup:     "clients/acme",
			wantRemaining: nil,
		},
		{
			name:          "group flag before subcommand",
			args:          []string{"-g", "work", "list"},
			wantGroup:     "work",
			wantRemaining: []string{"list"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			gotGroup, gotRemaining := extractGroupFlag(tt.args)
			if gotGroup != tt.wantGroup {
				t.Errorf("group: got %q, want %q", gotGroup, tt.wantGroup)
			}
			if len(gotRemaining) != len(tt.wantRemaining) {
				t.Errorf("remaining length: got %d (%v), want %d (%v)", len(gotRemaining), gotRemaining, len(tt.wantRemaining), tt.wantRemaining)
				return
			}
			for i, arg := range gotRemaining {
				if arg != tt.wantRemaining[i] {
					t.Errorf("remaining[%d]: got %q, want %q", i, arg, tt.wantRemaining[i])
				}
			}
		})
	}
}

func TestGroupScopeValidation(t *testing.T) {
	tests := []struct {
		input string
		want  string
	}{
		// normalizeGroupPath replaces spaces with hyphens but preserves case,
		// because GroupTree.Groups is keyed by the raw stored path.
		{"work", "work"},
		{"Work", "Work"},
		{"My Projects", "My-Projects"},
		{"work/frontend", "work/frontend"},
	}

	for _, tt := range tests {
		t.Run(tt.input, func(t *testing.T) {
			got := normalizeGroupPath(tt.input)
			if got != tt.want {
				t.Errorf("normalizeGroupPath(%q) = %q, want %q", tt.input, got, tt.want)
			}
		})
	}
}

func TestIsDuplicateSession(t *testing.T) {
	instances := []*session.Instance{
		{ID: "abc123", Title: "Test Session", ProjectPath: "/home/user/project"},
		{ID: "def456", Title: "Another Session", ProjectPath: "/home/user/other"},
		{ID: "ghi789", Title: "Root Session", ProjectPath: "/"},
	}

	tests := []struct {
		name      string
		title     string
		path      string
		expectDup bool
		expectID  string
	}{
		{
			name:      "exact duplicate",
			title:     "Test Session",
			path:      "/home/user/project",
			expectDup: true,
			expectID:  "abc123",
		},
		{
			name:      "same title different path",
			title:     "Test Session",
			path:      "/home/user/different",
			expectDup: false,
		},
		{
			name:      "different title same path",
			title:     "New Name",
			path:      "/home/user/project",
			expectDup: false,
		},
		{
			name:      "no duplicate",
			title:     "Unique Session",
			path:      "/home/user/unique",
			expectDup: false,
		},
		{
			name:      "trailing slash normalization - duplicate",
			title:     "Test Session",
			path:      "/home/user/project/",
			expectDup: true,
			expectID:  "abc123",
		},
		{
			name:      "root path duplicate",
			title:     "Root Session",
			path:      "/",
			expectDup: true,
			expectID:  "ghi789",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			isDup, inst := isDuplicateSession(instances, tt.title, tt.path)
			if isDup != tt.expectDup {
				t.Errorf("isDuplicateSession() isDup = %v, want %v", isDup, tt.expectDup)
			}
			if tt.expectDup && inst != nil && inst.ID != tt.expectID {
				t.Errorf("isDuplicateSession() returned instance ID = %q, want %q", inst.ID, tt.expectID)
			}
			if !tt.expectDup && inst != nil {
				t.Errorf("isDuplicateSession() returned instance when expecting no duplicate")
			}
		})
	}
}

func TestEnsureTmuxInPath(t *testing.T) {
	// ensureTmuxInPath should succeed when tmux is genuinely installed.
	if _, err := exec.LookPath("tmux"); err != nil {
		t.Skip("tmux not available")
	}

	t.Run("found_via_LookPath", func(t *testing.T) {
		if err := ensureTmuxInPath(); err != nil {
			t.Fatalf("ensureTmuxInPath() failed: %v", err)
		}
	})

	t.Run("found_via_fallback", func(t *testing.T) {
		// Discover the real tmux path so we can verify the fallback finds it.
		realPath, err := exec.LookPath("tmux")
		if err != nil {
			t.Skip("tmux not in PATH")
		}

		// Strip PATH down to something that definitely does NOT contain tmux,
		// then let ensureTmuxInPath try the fallback paths.
		origPath := os.Getenv("PATH")
		os.Setenv("PATH", "/nonexistent-dir-for-test")
		defer os.Setenv("PATH", origPath)

		err = ensureTmuxInPath()
		if err != nil {
			// Only fail if the real tmux lived in one of the fallback dirs.
			dir := filepath.Dir(realPath)
			wellKnown := []string{
				"/usr/bin",
				"/usr/local/bin",
				"/opt/homebrew/bin",
				"/home/linuxbrew/.linuxbrew/bin",
				"/snap/bin",
			}
			for _, d := range wellKnown {
				if d == dir {
					t.Fatalf("ensureTmuxInPath() should have found tmux at %s via fallback", realPath)
				}
			}
			t.Skipf("tmux at %s is not in a well-known fallback dir; fallback correctly failed", realPath)
		}

		// Verify that PATH was updated so LookPath now succeeds.
		if _, err := exec.LookPath("tmux"); err != nil {
			t.Fatalf("after ensureTmuxInPath(), exec.LookPath still fails: %v", err)
		}
	})

	t.Run("not_found_anywhere", func(t *testing.T) {
		origPath := os.Getenv("PATH")
		os.Setenv("PATH", "/nonexistent-dir-for-test")
		defer os.Setenv("PATH", origPath)

		// Temporarily check: if tmux is in a well-known path, this test can't
		// assert failure. Only run the assertion when no well-known path exists.
		wellKnown := []string{
			"/usr/bin/tmux",
			"/usr/local/bin/tmux",
			"/opt/homebrew/bin/tmux",
			"/home/linuxbrew/.linuxbrew/bin/tmux",
			"/snap/bin/tmux",
		}
		for _, p := range wellKnown {
			if _, err := os.Stat(p); err == nil {
				t.Skipf("tmux exists at well-known path %s; cannot test not-found case", p)
			}
		}

		if err := ensureTmuxInPath(); err == nil {
			t.Fatal("ensureTmuxInPath() should have failed when tmux is not installed")
		}
	})
}
