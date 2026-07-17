package ui

import (
	"testing"

	"github.com/asheshgoplani/agent-deck/internal/session"
)

func TestNewHomeDoesNotStartBackgroundWorkersInTests(t *testing.T) {
	if homeBackgroundWorkersEnabled {
		t.Fatal("TestMain must disable Home background workers")
	}
	claudeConfigDir := t.TempDir()
	t.Setenv("CLAUDE_CONFIG_DIR", claudeConfigDir)
	if _, err := session.InjectClaudeHooks(claudeConfigDir); err != nil {
		t.Fatalf("install test Claude hooks: %v", err)
	}

	home := NewHome()
	t.Cleanup(func() {
		if home.hookWatcher != nil {
			home.hookWatcher.Stop()
		}
		home.cancel()
		if home.storage != nil {
			_ = home.storage.Close()
		}
	})

	if home.statusWorkerDone != nil {
		t.Fatal("status worker channel is active in test mode")
	}
	if home.storageWatcher != nil {
		t.Fatal("storage watcher started in test mode")
	}
	if home.hookWatcher != nil {
		t.Fatal("hook watcher started in test mode")
	}
}
