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
	h := &Home{profile: session.GetEffectiveProfile("")}
	if got := h.watcherProfileForHome(); got != "work" {
		t.Fatalf("watcher Engine profile = %q, want configured default work", got)
	}
	h.profile = "explicit"
	if got := h.watcherProfileForHome(); got != "explicit" {
		t.Fatalf("watcher Engine profile = %q, want opened Home profile explicit", got)
	}
}
