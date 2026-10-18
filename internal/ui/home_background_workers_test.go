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

// TestHomeLogMaintenanceIsABackgroundWorker pins issue 2712: log maintenance
// shells out to tmux from a goroutine nobody waits for, so in test mode it ran
// tmux under a later test's swapped PATH and tripped that test's fake tmux.
func TestHomeLogMaintenanceIsABackgroundWorker(t *testing.T) {
	if homeBackgroundWorkersEnabled {
		t.Fatal("TestMain must disable Home background workers")
	}
	var launched []string
	orig := launchLogMaintenance
	launchLogMaintenance = func(name string) { launched = append(launched, name) }
	t.Cleanup(func() { launchLogMaintenance = orig })

	home := NewHome()
	t.Cleanup(func() {
		home.cancel()
		if home.storage != nil {
			_ = home.storage.Close()
		}
	})

	if len(launched) != 0 {
		t.Fatalf("log maintenance started in test mode: %v", launched)
	}

	homeBackgroundWorkersEnabled = true
	t.Cleanup(func() { homeBackgroundWorkersEnabled = false })
	startLogMaintenance("startup_log_maintenance")
	if len(launched) != 1 {
		t.Fatalf("log maintenance did not start with background workers enabled: %v", launched)
	}
}

// stubLogMaintenance keeps a test that turns Home background workers on from
// leaving a log-maintenance goroutine that runs tmux after the test ends.
func stubLogMaintenance(t *testing.T) {
	t.Helper()
	orig := launchLogMaintenance
	launchLogMaintenance = func(string) {}
	t.Cleanup(func() { launchLogMaintenance = orig })
}
