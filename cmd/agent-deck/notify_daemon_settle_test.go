package main

import (
	"context"
	"io"
	"log/slog"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/asheshgoplani/agent-deck/internal/update"
)

// The daemon's real start settles its own deferred launchd entry, and only
// from the service's main process (parent pid 1): an updater child that
// inherits XPC_SERVICE_NAME must never settle it.
func TestNotifyDaemonStart_SettlesOwnPendingFromMainProcessOnly(t *testing.T) {
	var got []time.Time
	prev := daemonSettlePending
	daemonSettlePending = func(_ update.RebootstrapOptions, startedAt time.Time) (bool, error) {
		got = append(got, startedAt)
		return true, nil
	}
	t.Cleanup(func() { daemonSettlePending = prev })
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	started := time.Date(2026, 10, 10, 2, 15, 29, 0, time.UTC)

	settleOwnPendingAtDaemonStart(4242, started, log)
	assert.Empty(t, got, "a child process must not settle")

	settleOwnPendingAtDaemonStart(1, started, log)
	require.Len(t, got, 1)
	assert.True(t, got[0].Equal(started))

	start := realNotifyDaemonStart()
	require.NotNil(t, start.settlePending, "the daemon start must settle its own pending entry")
	ran := make(chan struct{}, 1)
	start.watchVersion = func(context.Context, context.CancelFunc) {}
	start.headlessAutoInstall = func(context.Context) {}
	start.healUpdateTimer = func() {}
	start.settlePending = func() { ran <- struct{}{} }
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	start.begin(ctx, cancel)
	select {
	case <-ran:
	case <-time.After(5 * time.Second):
		t.Fatal("begin never settled the pending entry")
	}
}
