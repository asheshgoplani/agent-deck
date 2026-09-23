package main

import (
	"path/filepath"
	"testing"

	"github.com/asheshgoplani/agent-deck/internal/events"
	"github.com/asheshgoplani/agent-deck/internal/session"
)

func TestEventProfileUsesStorageFallbackForMissingInference(t *testing.T) {
	t.Cleanup(func() { events.SetProfile("default") })
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, "config"))
	t.Setenv("XDG_DATA_HOME", filepath.Join(home, "data"))
	t.Setenv("AGENTDECK_PROFILE", "")
	t.Setenv("CLAUDE_CONFIG_DIR", filepath.Join(home, ".claude-ghost"))
	if err := session.SaveConfig(&session.Config{DefaultProfile: "work"}); err != nil {
		t.Fatal(err)
	}
	if err := configureEventProfile(""); err != nil {
		t.Fatal(err)
	}
	got := events.CurrentProfile()
	if got != "work" {
		t.Fatalf("events profile = %q, want storage fallback work", got)
	}
}
