package ui

import (
	"path/filepath"
	"testing"

	"github.com/asheshgoplani/agent-deck/internal/session"
)

func TestWatcherHomeUsesConfiguredDefaultProfile(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(t.TempDir(), "config"))
	t.Setenv("XDG_DATA_HOME", filepath.Join(t.TempDir(), "data"))
	t.Setenv("AGENTDECK_PROFILE", "")
	t.Setenv("CLAUDE_CONFIG_DIR", "")
	if err := session.SaveConfig(&session.Config{DefaultProfile: "work"}); err != nil {
		t.Fatal(err)
	}
	if got := watcherProfileForHome(); got != "work" {
		t.Fatalf("watcher Engine profile = %q, want configured default work", got)
	}
}
