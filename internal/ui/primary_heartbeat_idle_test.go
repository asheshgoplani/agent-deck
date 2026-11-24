package ui

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/asheshgoplani/agent-deck/internal/session"
	"github.com/asheshgoplani/agent-deck/internal/statedb"
	"github.com/asheshgoplani/agent-deck/internal/tmux"
)

// primaryLease mirrors the timeout main.go passes to ElectPrimary for the
// allow_multiple=false single-instance gate.
const primaryLease = 30 * time.Second

// staleHeartbeat registers this process as primary and rewinds its heartbeat
// past the lease, standing in for 60s of a TUI sitting idle without a real
// sleep.
func staleHeartbeat(t *testing.T, db *statedb.StateDB) {
	t.Helper()
	if err := db.RegisterInstance(true); err != nil {
		t.Fatalf("RegisterInstance: %v", err)
	}
	if _, err := db.DB().Exec("UPDATE instance_heartbeats SET heartbeat = ? WHERE pid = ?",
		time.Now().Add(-60*time.Second).Unix(), os.Getpid()); err != nil {
		t.Fatalf("rewind heartbeat: %v", err)
	}
	if n, err := db.AliveInstanceCount(); err != nil || n != 0 {
		t.Fatalf("setup: AliveInstanceCount = %d, %v; want 0 after rewinding the heartbeat", n, err)
	}
}

// assertPrimaryHeld checks what a second TUI's ElectPrimary sees: a primary
// row whose heartbeat is inside the lease. That row is not cleared as stale,
// so the second instance (whose pidAlive check finds us alive) is refused.
func assertPrimaryHeld(t *testing.T, db *statedb.StateDB) {
	t.Helper()
	var pid int
	err := db.DB().QueryRow(
		"SELECT pid FROM instance_heartbeats WHERE is_primary = 1 AND heartbeat >= ?",
		time.Now().Add(-primaryLease).Unix(),
	).Scan(&pid)
	if err != nil || pid != os.Getpid() {
		t.Fatalf("after a sweep the primary's heartbeat is still older than %v (pid=%d err=%v): a second TUI would take primary while this one is alive", primaryLease, pid, err)
	}
	if n, err := db.AliveInstanceCount(); err != nil || n != 1 {
		t.Fatalf("AliveInstanceCount = %d, %v; want 1", n, err)
	}
}

// fakeTmuxOnPath answers every tmux call with success and no output, so the
// server-alive path runs without a real tmux server.
func fakeTmuxOnPath(t *testing.T) {
	t.Helper()
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "tmux"), []byte("#!/bin/sh\nexit 0\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	tmux.ResetSocketSessionCacheForTest()
	t.Cleanup(tmux.ResetSocketSessionCacheForTest)
}

// A primary TUI with no sessions must keep heartbeating; before the fix the
// len(instances)==0 early return skipped the heartbeat, so after 30s a second
// TUI passed the allow_multiple=false gate.
func TestBackgroundStatusUpdate_HeartbeatsWithZeroSessions(t *testing.T) {
	db := newClaimsTestDB(t)
	fakeTmuxOnPath(t)
	tmux.SetServerAliveForTest(t, true)
	staleHeartbeat(t, db)

	h := newHomeForSnapshotTest()
	h.backgroundStatusUpdate()

	assertPrimaryHeld(t, db)
}

// Same with the tmux server down: the IsServerAlive early return also ran
// before the heartbeat.
func TestBackgroundStatusUpdate_HeartbeatsWithTmuxServerDown(t *testing.T) {
	db := newClaimsTestDB(t)
	fakeTmuxOnPath(t)
	tmux.SetServerAliveForTest(t, false)
	staleHeartbeat(t, db)

	h := newHomeForSnapshotTest()
	h.backgroundStatusUpdate()

	assertPrimaryHeld(t, db)
}

// Sibling early return: during key navigation (hot window) the sweep skips its
// work, and it must not skip the heartbeat with it.
func TestBackgroundStatusUpdate_HeartbeatsDuringNavigationHotWindow(t *testing.T) {
	db := newClaimsTestDB(t)
	fakeTmuxOnPath(t)
	tmux.SetServerAliveForTest(t, true)
	staleHeartbeat(t, db)

	h := newHomeForSnapshotTest()
	h.navigationHotUntil.Store(time.Now().Add(time.Minute).UnixNano())
	h.backgroundStatusUpdate()

	assertPrimaryHeld(t, db)
}

// Issue #3268: a headless web server polls no session status, so its sweep
// must not heartbeat. A fresh row would make notify-daemon read it as a live
// TUI, stop probing tmux and miss every child turn end.
func TestBackgroundStatusUpdate_HeadlessDoesNotHeartbeat(t *testing.T) {
	db := newClaimsTestDB(t)
	fakeTmuxOnPath(t)
	tmux.SetServerAliveForTest(t, true)
	if err := db.RegisterInstance(false); err != nil {
		t.Fatalf("RegisterInstance: %v", err)
	}
	if _, err := db.DB().Exec("UPDATE instance_heartbeats SET heartbeat = ? WHERE pid = ?",
		time.Now().Add(-60*time.Second).Unix(), os.Getpid()); err != nil {
		t.Fatalf("rewind heartbeat: %v", err)
	}

	h := newHomeForSnapshotTest()
	h.SetHeadless(true)
	h.backgroundStatusUpdate()

	if n, err := db.AliveInstanceCount(); err != nil || n != 0 {
		t.Fatalf("after a headless sweep AliveInstanceCount = %d, %v; want 0", n, err)
	}
}

// Issue #3268, review: a web mutation hydrates the headless Home's instances
// from storage (#1397), but that snapshot is not a status poller. A headless
// sweep must not probe or persist it: with no live TUI, notify-daemon owns
// status, and a second writer would race it with a stale subset of the
// fleet. A TUI sweep of the same instance persists its status.
func TestBackgroundStatusUpdate_HeadlessDoesNotPollHydratedInstances(t *testing.T) {
	for _, tc := range []struct {
		name        string
		headless    bool
		wantPersist bool
	}{
		{name: "tui", headless: false, wantPersist: true},
		{name: "headless web", headless: true, wantPersist: false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			newClaimsTestDB(t)
			fakeTmuxOnPath(t)
			tmux.SetServerAliveForTest(t, true)

			h := newHomeForSnapshotTest()
			h.lastPersistedStatus = make(map[string]string)
			h.SetHeadless(tc.headless)
			h.instances = []*session.Instance{{ID: "hydrated", Title: "hydrated", Tool: "claude", Status: session.StatusIdle}}
			h.backgroundStatusUpdate()

			if _, persisted := h.lastPersistedStatus["hydrated"]; persisted != tc.wantPersist {
				t.Fatalf("sweep persisted the hydrated instance's status = %v, want %v", persisted, tc.wantPersist)
			}
		})
	}
}

// Issue #3268, review: when main's early storage open did not set the global
// StateDB, NewHomeWithProfileAndMode registers the instance itself. A headless
// web server must not register there either, and its Home is headless from
// construction, before the status worker can read the flag.
func TestNewHome_HeadlessDoesNotRegisterHeartbeat(t *testing.T) {
	for _, tc := range []struct {
		name      string
		headless  bool
		wantAlive int
	}{
		{name: "tui", headless: false, wantAlive: 1},
		{name: "headless web", headless: true, wantAlive: 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			root := t.TempDir()
			t.Setenv("HOME", root)
			for _, k := range []string{"XDG_CONFIG_HOME", "XDG_DATA_HOME", "XDG_CACHE_HOME", "XDG_STATE_HOME"} {
				t.Setenv(k, filepath.Join(root, k))
			}
			t.Setenv("AGENTDECK_PROFILE", "")
			prev := statedb.GetGlobal()
			statedb.SetGlobal(nil)
			t.Cleanup(func() { statedb.SetGlobal(prev) })

			h := NewHomeWithProfileAndMode("", tc.headless)
			t.Cleanup(func() {
				h.cancel()
				if h.storage != nil {
					h.storage.Close()
				}
			})
			if h.IsHeadless() != tc.headless {
				t.Fatalf("IsHeadless() = %v right after construction, want %v", h.IsHeadless(), tc.headless)
			}
			db := statedb.GetGlobal()
			if db == nil {
				t.Fatal("NewHomeWithProfileAndMode left no global StateDB")
			}
			if n, err := db.AliveInstanceCount(); err != nil || n != tc.wantAlive {
				t.Fatalf("AliveInstanceCount = %d, %v; want %d", n, err, tc.wantAlive)
			}
		})
	}
}
