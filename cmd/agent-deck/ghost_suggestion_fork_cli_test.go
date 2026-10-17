package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/asheshgoplani/agent-deck/internal/git"
	"github.com/asheshgoplani/agent-deck/internal/session"
)

// `session fork` builds a root fork (no parent, even when the source is a
// worker), and its first start runs the command baked before Start(). Under
// children-off that command leaves prompt suggestions alone; with suggestions
// forced off in config the baked command already carries the export, so the
// first launch honours it and not only later restarts.
func TestSessionFork_CLIPromptSuggestionsInBakedCommand(t *testing.T) {
	const export = "export CLAUDE_CODE_ENABLE_PROMPT_SUGGESTION=false;"
	for _, tc := range []struct {
		name    string
		config  string
		wantOff bool
	}{
		{"children-off keeps a root fork untouched", "", false},
		{"config off reaches the first launch", "[launch]\nprompt_suggestions = \"off\"\n", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			home := t.TempDir()
			t.Setenv("HOME", home)
			configDir := filepath.Join(home, ".agent-deck")
			if err := os.MkdirAll(configDir, 0o755); err != nil {
				t.Fatalf("mkdir config dir: %v", err)
			}
			if err := os.WriteFile(filepath.Join(configDir, "config.toml"), []byte(tc.config), 0o600); err != nil {
				t.Fatalf("write config: %v", err)
			}
			session.ClearUserConfigCache()
			t.Cleanup(session.ClearUserConfigCache)

			source := session.NewInstanceWithGroupAndTool("worker", home, "grp", "claude")
			source.ClaudeSessionID = "00000000-0000-4000-8000-000000000001"
			source.ClaudeDetectedAt = time.Now()
			source.SetParentWithPath("conductor-1600000000", "/tmp/conductor")

			profile := "fork_prompt_suggestions"
			storage, err := session.NewStorageWithProfile(profile)
			if err != nil {
				t.Fatalf("NewStorageWithProfile: %v", err)
			}
			t.Cleanup(func() { _ = storage.Close() })
			if err := storage.SaveWithGroups([]*session.Instance{source}, session.NewGroupTreeWithGroups([]*session.Instance{source}, nil)); err != nil {
				t.Fatalf("SaveWithGroups: %v", err)
			}

			var forked *session.Instance
			oldHook := sessionForkBeforeStartHook
			sessionForkBeforeStartHook = func(_ *session.Instance, f *session.Instance, _ git.WorktreeStateOptions) { forked = f }
			t.Cleanup(func() { sessionForkBeforeStartHook = oldHook })

			handleSessionFork(profile, []string{"worker", "-t", "worker-fork"})

			if forked == nil {
				t.Fatal("CLI fork did not reach the pre-start hook")
			}
			if forked.ParentSessionID != "" {
				t.Fatalf("CLI fork must be a root session, got parent %q", forked.ParentSessionID)
			}
			if !forked.IsForkAwaitingStart {
				t.Fatal("first start must run the baked fork command")
			}
			if got := strings.Contains(forked.Command, export); got != tc.wantOff {
				t.Errorf("baked fork command carries the export = %v, want %v: %s", got, tc.wantOff, forked.Command)
			}
		})
	}
}
