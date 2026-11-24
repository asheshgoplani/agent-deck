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
			isolateHomeAndXDG(t)
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

// Issue #3268, review: the headless sweep polls no status, but it still
// enforces idle timeouts (#1143). It reads the sessions from storage on each
// due tick rather than the snapshot a web mutation hydrated, so a session
// added after the last mutation is covered too. While a TUI is alive, the
// TUI's own watcher enforces them and the headless one stays out.
func TestBackgroundStatusUpdate_HeadlessEnforcesIdleTimeoutsFromStorage(t *testing.T) {
	for _, tc := range []struct {
		name, profile string
		tuiAlive      bool
		wantStopped   int
	}{
		{name: "no tui", profile: "_test_3268_idle_notui", tuiAlive: false, wantStopped: 1},
		{name: "live tui", profile: "_test_3268_idle_tui", tuiAlive: true, wantStopped: 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			isolateHomeAndXDG(t)
			db := newClaimsTestDB(t)
			fakeTmuxOnPath(t)
			tmux.SetServerAliveForTest(t, true)
			if tc.tuiAlive {
				if err := db.RegisterInstance(false); err != nil {
					t.Fatalf("RegisterInstance: %v", err)
				}
			}

			stubIdleStatusProbe(t, func(*session.Instance) {})
			h, storage := newHeadlessHomeForTest(t, tc.profile)
			armed := &session.Instance{
				ID: "idle-3268", Title: "idle-3268", ProjectPath: t.TempDir(), GroupPath: session.DefaultGroupPath,
				Command: "bash", Tool: "bash", Status: session.StatusWaiting, IdleTimeoutSecs: 60, CreatedAt: time.Now(),
			}
			if err := storage.SaveWithGroups([]*session.Instance{armed}, session.NewGroupTree([]*session.Instance{armed})); err != nil {
				t.Fatalf("seed: %v", err)
			}
			now := time.Now()
			var stopped []string
			h.idleTimeoutWatcher = session.NewIdleTimeoutWatcher(session.IdleTimeoutWatcherConfig{
				Now:      func() time.Time { return now },
				Capture:  func(*session.Instance) (string, error) { return "same pane", nil },
				Stop:     func(inst *session.Instance) error { stopped = append(stopped, inst.ID); return nil },
				LogEvent: func(session.SessionLifecycleEvent) error { return nil },
			})

			h.backgroundStatusUpdate() // first due tick: records the pane
			now = now.Add(61 * time.Second)
			h.idleTimeoutLastTick.Store(0) // next tick is due
			h.backgroundStatusUpdate()

			if len(stopped) != tc.wantStopped {
				t.Fatalf("headless sweep stopped %v, want %d stop(s) after 61s without pane change (idle_timeout_secs=60)", stopped, tc.wantStopped)
			}
		})
	}
}

// isolateHomeAndXDG points HOME and every XDG root at a fresh temp dir, so
// storage opened by the test is its own.
func isolateHomeAndXDG(t *testing.T) {
	t.Helper()
	root := t.TempDir()
	t.Setenv("HOME", root)
	for _, k := range []string{"XDG_CONFIG_HOME", "XDG_DATA_HOME", "XDG_CACHE_HOME", "XDG_STATE_HOME"} {
		t.Setenv(k, filepath.Join(root, k))
	}
}

// seedArmedSession saves one bash session with a 60 s idle timeout.
func seedArmedSession(t *testing.T, storage *session.Storage, status session.Status) *session.Instance {
	t.Helper()
	armed := &session.Instance{
		ID: "idle-3268", Title: "idle-3268", ProjectPath: t.TempDir(), GroupPath: session.DefaultGroupPath,
		Command: "bash", Tool: "bash", Status: status, IdleTimeoutSecs: 60, CreatedAt: time.Now(),
	}
	if err := storage.SaveWithGroups([]*session.Instance{armed}, session.NewGroupTree([]*session.Instance{armed})); err != nil {
		t.Fatalf("seed: %v", err)
	}
	return armed
}

// idleClock drives a headless Home's idle-timeout watcher with a fake clock
// and an unchanging pane, recording what it stops.
type idleClock struct {
	now     time.Time
	stopped []string
}

func newIdleClock(t *testing.T, h *Home) *idleClock {
	t.Helper()
	stubIdleStatusProbe(t, func(*session.Instance) {})
	c := &idleClock{now: time.Now()}
	h.idleTimeoutWatcher = session.NewIdleTimeoutWatcher(session.IdleTimeoutWatcherConfig{
		Now:      func() time.Time { return c.now },
		Capture:  func(*session.Instance) (string, error) { return "same pane", nil },
		Stop:     func(inst *session.Instance) error { c.stopped = append(c.stopped, inst.ID); return nil },
		LogEvent: func(session.SessionLifecycleEvent) error { return nil },
	})
	return c
}

// sweepAt runs one due sweep at offset from the start.
func (c *idleClock) sweepAt(h *Home, start time.Time, offset time.Duration) {
	c.now = start.Add(offset)
	h.idleTimeoutLastTick.Store(0)
	h.backgroundStatusUpdate()
}

// Issue #3268, review: a TUI that owned the timeouts for a while hands back
// a fresh idle clock. The headless watcher forgets its baselines while the
// TUI is alive, so a pane that changed and came back to the same frame
// meanwhile is not stopped on the first headless tick after the TUI exits.
func TestBackgroundStatusUpdate_HeadlessIdleClockRestartsAfterTUIHandoff(t *testing.T) {
	isolateHomeAndXDG(t)
	db := newClaimsTestDB(t)
	fakeTmuxOnPath(t)
	tmux.SetServerAliveForTest(t, true)
	h, storage := newHeadlessHomeForTest(t, "_test_3268_handoff")
	seedArmedSession(t, storage, session.StatusWaiting)
	c := newIdleClock(t, h)
	start := c.now

	c.sweepAt(h, start, 0) // headless records the pane
	if err := db.RegisterInstance(false); err != nil {
		t.Fatalf("RegisterInstance: %v", err)
	}
	c.sweepAt(h, start, 30*time.Second) // a TUI owns the timeouts
	if err := db.UnregisterInstance(); err != nil {
		t.Fatalf("UnregisterInstance: %v", err)
	}
	c.sweepAt(h, start, 61*time.Second) // TUI gone: the clock starts here
	if len(c.stopped) != 0 {
		t.Fatalf("first headless tick after the TUI exited stopped %v on a pre-TUI baseline, want none", c.stopped)
	}
	c.sweepAt(h, start, 122*time.Second)
	if len(c.stopped) != 1 {
		t.Fatalf("61s after the handoff the headless watcher stopped %v, want the armed session", c.stopped)
	}
}

// Issue #3268, review: with headless web alone (no TUI, no notify-daemon)
// no one writes a hookless session's status row, so it can stay at the
// starting it was saved with. The headless tick probes armed sessions
// itself, so such a session still becomes eligible and is stopped.
func TestBackgroundStatusUpdate_HeadlessIdleTimeoutProbesSavedStatus(t *testing.T) {
	isolateHomeAndXDG(t)
	newClaimsTestDB(t)
	fakeTmuxOnPath(t)
	tmux.SetServerAliveForTest(t, true)
	h, storage := newHeadlessHomeForTest(t, "_test_3268_starting")
	seedArmedSession(t, storage, session.StatusStarting)
	c := newIdleClock(t, h)
	probed := 0
	stubIdleStatusProbe(t, func(inst *session.Instance) { // the live pane reads waiting
		probed++
		inst.SetStatusThreadSafe(session.StatusWaiting)
	})
	start := c.now

	c.sweepAt(h, start, 0)
	c.sweepAt(h, start, 61*time.Second)
	if probed != 2 || len(c.stopped) != 1 {
		t.Fatalf("a session saved as starting: probed %d times, stopped %v after 61s idle; want 2 probes and the armed session stopped", probed, c.stopped)
	}
}

// Issue #3268, review: a headless idle stop writes stopped to the row right
// after the kill (defect 317), since nothing else persists the instance it
// loaded for the tick. A TUI stop leaves the row to its next sweep.
func TestHome_IdleTimeoutStopPersistsStoppedWhenHeadless(t *testing.T) {
	for _, tc := range []struct {
		name, profile string
		headless      bool
		wantRow       string
	}{
		{name: "headless web", profile: "_test_3268_stop_headless", headless: true, wantRow: string(session.StatusStopped)},
		{name: "tui", profile: "_test_3268_stop_tui", headless: false, wantRow: string(session.StatusWaiting)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			isolateHomeAndXDG(t)
			fakeTmuxOnPath(t)
			h, storage := newHeadlessHomeForTest(t, tc.profile)
			h.headless = tc.headless
			seedArmedSession(t, storage, session.StatusWaiting)
			loaded, _, err := storage.LoadWithGroups()
			if err != nil || len(loaded) != 1 {
				t.Fatalf("load: %v (%d)", err, len(loaded))
			}

			if err := h.idleTimeoutStop(loaded[0]); err != nil {
				t.Fatalf("idleTimeoutStop: %v", err)
			}
			if got := session.SavedRowStatus(storage, loaded[0].ID); got != tc.wantRow {
				t.Fatalf("row after the idle stop = %q, want %q", got, tc.wantRow)
			}
		})
	}
}

// stubIdleStatusProbe replaces the headless idle tick's status probe: a fake
// tmux cannot answer it.
func stubIdleStatusProbe(t *testing.T, probe func(*session.Instance)) {
	t.Helper()
	prev := headlessIdleStatusProbe
	headlessIdleStatusProbe = probe
	t.Cleanup(func() { headlessIdleStatusProbe = prev })
}
