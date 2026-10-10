package update

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// 2026-10-10: the notify daemon's updater installed v1.16.27 from inside
// com.agentdeck.transition-notifier and deferred that agent (02:15:02);
// launchd brought the daemon back on the new binary 27 s later, yet every
// later run inside the service kept the entry ("this process runs inside
// it"), so the agent read "pending" until the daily timer or a ticking
// TUI drained it. The service's own fresh process is the proof the
// re-registration was meant to get: it settles the entry itself.
func TestSettleRespawnedService_DropsOwnEntryOnceRespawned(t *testing.T) {
	t.Setenv(launchdServiceEnv, "")
	pending := pendingPath(t)
	const notifier = "com.agentdeck.transition-notifier"
	since := time.Date(2026, 10, 10, 2, 15, 2, 0, time.UTC)
	require.NoError(t, addPendingRebootstrap(pending, notifier, since))
	require.NoError(t, addPendingRebootstrap(pending, "com.agentdeck.web", since))

	opts := RebootstrapOptions{
		GOOS: "darwin", ExePath: "/bin/agent-deck", LaunchAgentsDir: t.TempDir(), UID: 501,
		Runner: newFakeRunner(), Logger: discardLogger(), PendingPath: pending, ServiceLabel: notifier,
	}

	// A process that was already running when the entry was written (the
	// old daemon, or a child of it) proves nothing.
	settled, err := SettleRespawnedService(opts, since.Add(-time.Minute))
	require.NoError(t, err)
	assert.False(t, settled)
	labels, _ := PendingRebootstrap(pending)
	assert.Equal(t, []string{notifier, "com.agentdeck.web"}, labels)

	// Outside launchd (no service label) nothing is settled either.
	outside := opts
	outside.ServiceLabel = ""
	settled, err = SettleRespawnedService(outside, since.Add(time.Minute))
	require.NoError(t, err)
	assert.False(t, settled)

	// The service's process started after the deferral: its own entry is
	// settled, every other label is left for a run outside it.
	settled, err = SettleRespawnedService(opts, since.Add(27*time.Second))
	require.NoError(t, err)
	assert.True(t, settled)
	labels, _ = PendingRebootstrap(pending)
	assert.Equal(t, []string{"com.agentdeck.web"}, labels)

	// Not darwin: a no-op.
	linux := opts
	linux.GOOS = "linux"
	linux.ServiceLabel = "com.agentdeck.web"
	settled, err = SettleRespawnedService(linux, since.Add(time.Hour))
	require.NoError(t, err)
	assert.False(t, settled)
}

// Only a deferral is settled by a respawn: an agent a run booted out and
// could not bring back keeps its retry record.
func TestSettleRespawnedService_KeepsBootstrapFailures(t *testing.T) {
	pending := pendingPath(t)
	const web = "com.agentdeck.web"
	since := time.Date(2026, 10, 10, 2, 15, 2, 0, time.UTC)
	require.NoError(t, notePendingFailure(pending, web, assert.AnError, since))

	opts := RebootstrapOptions{
		GOOS: "darwin", ExePath: "/bin/agent-deck", LaunchAgentsDir: t.TempDir(), UID: 501,
		Runner: newFakeRunner(), Logger: discardLogger(), PendingPath: pending, ServiceLabel: web,
	}
	settled, err := SettleRespawnedService(opts, since.Add(time.Minute))
	require.NoError(t, err)
	assert.False(t, settled)
	labels, _ := PendingRebootstrap(pending)
	assert.Equal(t, []string{web}, labels)
}
