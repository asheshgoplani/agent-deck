package ui

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/asheshgoplani/agent-deck/internal/session"
	"github.com/asheshgoplani/agent-deck/internal/update"
)

// TestAutoUpdate_RestartWaitsForSweepAndMarkerClears replays the v1.16.11
// rollout end to end, with the updater in-process: the periodic check
// starts `update --unattended`, which installs, takes update.lock and marks
// a remote sweep; the new binary lands on disk and auto_restart wants to
// re-exec; the restart is deferred until the child has finished its
// sweep; then the marker and the lock are gone and the restart proceeds.
func TestAutoUpdate_RestartWaitsForSweepAndMarkerClears(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("XDG_DATA_HOME", t.TempDir())
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	t.Setenv(update.SkipUpdateCheckEnv, "")
	stubUpdateSettings(t, session.UpdateSettings{})
	lockDir := t.TempDir()
	lockPath := filepath.Join(lockDir, update.UpdateLockFileName)

	sweeping := make(chan struct{})
	finish := make(chan struct{})
	prev := runUnattendedUpdate
	runUnattendedUpdate = func(ctx context.Context, exe string) (string, error) {
		// What the child does after "installed": lock held, sweep marked,
		// remotes being deployed to (blocked here until the test says so).
		release, busy, err := update.AcquireUpdateLock(lockDir, update.UpdateLockStaleAfter)
		if err != nil || busy {
			t.Errorf("child could not take update.lock: busy=%v err=%v", busy, err)
			return "", err
		}
		defer release()
		end, err := session.BeginRemoteSweep([]string{"lab", "prod"})
		if err != nil {
			t.Errorf("child could not mark the sweep: %v", err)
			return "", err
		}
		defer end()
		close(sweeping)
		select {
		case <-finish:
		case <-ctx.Done():
			return "", ctx.Err()
		}
		return "✓ Updated to v1.16.1 (unattended)\n", nil
	}
	t.Cleanup(func() { runUnattendedUpdate = prev })

	h := newRestartTestHome(t)
	h.updateInfo = &update.UpdateInfo{Available: true, CurrentVersion: "1.16.0", LatestVersion: "1.16.1"}
	cmd := h.maybeAutoInstall(h.updateInfo)
	if cmd == nil || h.autoInstallInFlight != "1.16.1" {
		t.Fatalf("unattended install did not start (cmd=%v inFlight=%q)", cmd, h.autoInstallInFlight)
	}
	done := make(chan unattendedInstallFinishedMsg, 1)
	go func() { done <- cmd().(unattendedInstallFinishedMsg) }()
	select {
	case <-sweeping:
	case <-time.After(5 * time.Second):
		t.Fatal("child never reached its sweep")
	}
	if _, ok := session.RemoteSweepInProgress(); !ok {
		t.Fatal("the child's sweep marker must be live while it runs")
	}

	// The install landed: the binary watch sees a newer build.
	h.binaryWatch.observe(fpAt(2, 2))
	h.binaryWatch.recordProbe(fpAt(2, 2), "1.16.1", nil)
	if cmd := h.maybeAutoRestart(); cmd != nil || h.restartRequested || h.isQuitting {
		t.Fatal("auto restart must be deferred while the update child sweeps")
	}
	assertRestartBlocked(t, h, "unattended update to v1.16.1 is still running")
	h.err = nil

	// The sweep completes.
	close(finish)
	var msg unattendedInstallFinishedMsg
	select {
	case msg = <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("child never exited")
	}
	if msg.err != nil || !strings.Contains(msg.output, "Updated") {
		t.Fatalf("child result = %+v", msg)
	}
	if _, ok := session.RemoteSweepInProgress(); ok {
		t.Fatal("sweep marker must be cleared once the child is done")
	}
	if _, err := os.Stat(lockPath); !os.IsNotExist(err) {
		t.Fatalf("update.lock must be released, stat err = %v", err)
	}
	h.handleUnattendedInstallFinished(msg)
	if cmd := h.maybeAutoRestart(); cmd == nil || !h.restartRequested {
		t.Fatal("with the child gone the restart must be armed")
	}
	if exe, ok := h.RestartTarget(); !ok || exe != "/bin/agent-deck" {
		t.Fatalf("RestartTarget = %q, %v", exe, ok)
	}
}
