package session

import (
	"testing"
	"time"
)

// LastSpawnAt is the only cross-process record that a session was just
// (re)started: `agent-deck session restart` exits right after respawning the
// pane, and the tmux.Session a later `session send` process reconnects has its
// startup window explicitly zeroed. Without this reader, the send path cannot
// tell a booting agent from an idle one, types into a half-mounted TUI, and the
// message is silently dropped.
func TestLastSpawnAt_ReportsRecordedSpawn(t *testing.T) {
	withTempLockDir(t)

	if _, ok := LastSpawnAt("inst-never-spawned"); ok {
		t.Fatal("an instance with no stamp must report no spawn time")
	}

	before := time.Now().Add(-time.Second)
	recordInstanceSpawn("inst-spawn-stamp")
	after := time.Now().Add(time.Second)

	got, ok := LastSpawnAt("inst-spawn-stamp")
	if !ok {
		t.Fatal("expected a spawn time after recordInstanceSpawn")
	}
	if got.Before(before) || got.After(after) {
		t.Fatalf("spawn stamp %s outside the expected window [%s, %s]", got, before, after)
	}
}

// The stamp advances on every spawn: a restart must not read as the original
// start, or the post-spawn settle gate would skip the very window it guards.
func TestLastSpawnAt_AdvancesOnRespawn(t *testing.T) {
	withTempLockDir(t)

	prevNow := nowFn
	t.Cleanup(func() { nowFn = prevNow })

	base := time.Now().Add(-time.Hour)
	nowFn = func() time.Time { return base }
	recordInstanceSpawn("inst-restarted")
	first, ok := LastSpawnAt("inst-restarted")
	if !ok {
		t.Fatal("expected a stamp after the first spawn")
	}

	nowFn = func() time.Time { return base.Add(30 * time.Minute) }
	recordInstanceSpawn("inst-restarted")
	second, ok := LastSpawnAt("inst-restarted")
	if !ok {
		t.Fatal("expected a stamp after the restart")
	}
	if !second.After(first) {
		t.Fatalf("restart stamp %s did not advance past the start stamp %s", second, first)
	}
}
