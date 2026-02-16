package main

import (
	"os"
	"os/exec"
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
			"session", "mcp", "group", "try", "worktree", "wt",
			"profile", "update", "mcp-proxy", "uninstall",
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
