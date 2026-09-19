package update

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestTUIHeartbeat_RoundTripAndDeadPidsPruned(t *testing.T) {
	dir := t.TempDir()
	now := time.Date(2026, 9, 19, 14, 45, 0, 0, time.UTC)
	old := TUIHeartbeat{PID: 94928, Version: "1.16.11-rc.6", StartedAt: now.Add(-9 * 24 * time.Hour), UpdatedAt: now, LastTickAt: now.Add(-10 * time.Second),
		InstalledVersion: "1.16.12", InstalledSince: now.Add(-10 * time.Minute), RestartState: "waiting", BlockReason: "close the open dialog first"}
	attached := TUIHeartbeat{PID: 87692, Version: "1.16.11", StartedAt: now.Add(-2 * time.Hour), UpdatedAt: now, LastTickAt: now.Add(-14 * time.Minute)}
	dead := TUIHeartbeat{PID: 51055, Version: "1.16.11", StartedAt: now, UpdatedAt: now}
	for _, hb := range []TUIHeartbeat{old, attached, dead} {
		require.NoError(t, WriteTUIHeartbeat(dir, hb))
	}
	require.NoError(t, os.WriteFile(filepath.Join(dir, TUIHeartbeatDirName, "junk.json"), []byte("{"), 0o644))

	alive := func(pid int) bool { return pid != 51055 }
	hbs, err := ListTUIHeartbeats(dir, alive)
	require.NoError(t, err)
	require.Len(t, hbs, 2)
	assert.Equal(t, 87692, hbs[0].PID)
	assert.Equal(t, 94928, hbs[1].PID)
	assert.NoFileExists(t, tuiHeartbeatPath(dir, 51055), "a dead pid's file is removed")

	reports := ReportTUIs(hbs, "1.16.12", now)
	require.Len(t, reports, 2)
	assert.Equal(t, TUIReport{PID: 87692, Version: "1.16.11", Outdated: true, Ticking: false,
		StartedAt: attached.StartedAt.Format(time.RFC3339), UpdatedAt: now.Format(time.RFC3339)}, reports[0])
	assert.True(t, reports[1].Outdated)
	assert.True(t, reports[1].Ticking)
	assert.Equal(t, "waiting", reports[1].RestartState)
	assert.Equal(t, "close the open dialog first", reports[1].BlockReason)
	assert.Equal(t, int64(600), reports[1].OutdatedForSeconds)
	assert.Equal(t, "pid 87692 v1.16.11 (outdated, not ticking: attached or hung)", DescribeTUIReport(reports[0]))
	assert.Equal(t, "pid 94928 v1.16.11-rc.6 (outdated, restart waiting: close the open dialog first)", DescribeTUIReport(reports[1]))

	// Same version, or a local build of it, is not outdated.
	cur := ReportTUIs([]TUIHeartbeat{{PID: 1, Version: "1.16.12+local.abc", LastTickAt: now}}, "1.16.12", now)
	assert.False(t, cur[0].Outdated)
	assert.Equal(t, "pid 1 v1.16.12+local.abc (current)", DescribeTUIReport(cur[0]))

	require.NoError(t, RemoveTUIHeartbeat(dir, 94928))
	require.NoError(t, RemoveTUIHeartbeat(dir, 94928), "removing twice is fine")
	hbs, _ = ListTUIHeartbeats(dir, alive)
	assert.Len(t, hbs, 1)

	// No dir at all: nothing, no error.
	hbs, err = ListTUIHeartbeats(t.TempDir(), alive)
	require.NoError(t, err)
	assert.Empty(t, hbs)
}
