//go:build !windows

package main

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/asheshgoplani/agent-deck/internal/session"
)

// run-task called without --profile and outside an agent-deck pane (cron, a
// plain shell with --child) stamped the completion record with the literal
// "default" profile instead of the profile every other command resolves
// (config.json default_profile here), so the parent's daemon never saw it.
func TestRunTask_NoProfileFlagOrEnvUsesConfiguredDefaultProfile(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))
	t.Setenv("XDG_DATA_HOME", filepath.Join(home, ".local", "share"))
	t.Setenv("XDG_STATE_HOME", filepath.Join(home, ".local", "state"))
	t.Setenv("XDG_CACHE_HOME", filepath.Join(home, ".cache"))
	t.Setenv("AGENTDECK_EVENTS_BUS", "off")
	t.Setenv("AGENTDECK_PROFILE", "")
	t.Setenv("AGENTDECK_INSTANCE_ID", "")
	t.Setenv("AGENTDECK_TITLE", "")
	t.Setenv("CLAUDE_CONFIG_DIR", "")
	t.Setenv("TMUX", "")

	// A tmux that never reaches a real server.
	fakeBin := t.TempDir()
	if err := os.WriteFile(filepath.Join(fakeBin, "tmux"), []byte("#!/bin/sh\nexit 1\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", fakeBin+string(os.PathListSeparator)+os.Getenv("PATH"))

	if err := session.SaveConfig(&session.Config{DefaultProfile: "work"}); err != nil {
		t.Fatal(err)
	}

	captureStdout(t, func() {
		handleRunTask([]string{"--child", "child-runtask-profile", "--", "true"})
	})

	work, err := session.LoadCompletionRecords("work")
	if err != nil {
		t.Fatal(err)
	}
	def, err := session.LoadCompletionRecords(session.DefaultProfile)
	if err != nil {
		t.Fatal(err)
	}
	if len(work) != 1 || len(def) != 0 {
		t.Fatalf("completion records: %d in work, %d in default; want the record in the configured default profile work", len(work), len(def))
	}
}
