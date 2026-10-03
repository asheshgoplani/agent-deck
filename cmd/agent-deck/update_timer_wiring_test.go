package main

import (
	"context"
	"errors"
	"log/slog"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/asheshgoplani/agent-deck/internal/session"
	"github.com/asheshgoplani/agent-deck/internal/update"
)

// #2472 verifier round 1, finding 2: the automatic timer heal is wired into
// the daemon start, the real unattended run and the explicit remote update.
// Each test drives the production wiring and fails when it is removed.

// The notify daemon's real start sequence heals the timer exactly once,
// through the daemonEnsureUpdateTimer seam.
func TestNotifyDaemonStart_HealsTheTimerExactlyOnce(t *testing.T) {
	var calls atomic.Int32
	healed := make(chan struct{}, 4)
	prev := daemonEnsureUpdateTimer
	daemonEnsureUpdateTimer = func(*slog.Logger) (update.TimerEnsureResult, error) {
		calls.Add(1)
		healed <- struct{}{}
		return update.TimerEnsureResult{Action: update.TimerActionNone}, nil
	}
	t.Cleanup(func() { daemonEnsureUpdateTimer = prev })

	start := realNotifyDaemonStart()
	require.NotNil(t, start.healUpdateTimer, "the daemon start must heal the update timer")
	// The other start work is replaced so the test touches nothing real.
	var watched, polled atomic.Int32
	start.watchVersion = func(context.Context, context.CancelFunc) { watched.Add(1) }
	start.headlessAutoInstall = func(context.Context) { polled.Add(1) }

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	start.begin(ctx, cancel)

	select {
	case <-healed:
	case <-time.After(5 * time.Second):
		t.Fatal("the daemon start never healed the update timer")
	}
	// A second heal from the same start would land well within this.
	time.Sleep(200 * time.Millisecond)
	assert.Equal(t, int32(1), calls.Load(), "one heal per daemon start")
	assert.Equal(t, int32(1), polled.Load())
}

// The real unattended run heals the timer through AutoEnsureUpdateTimer;
// under go test that answers with the suppression skip, which proves the
// wiring without touching the host's init system.
func TestRealUnattendedDeps_WiresTheTimerHeal(t *testing.T) {
	setupTask6XDGEnv(t)
	deps, closeLog := realUnattendedDeps("timer")
	t.Cleanup(closeLog)
	require.NotNil(t, deps.ensureTimer, "update --unattended must install or heal the timer")
	res, err := deps.ensureTimer()
	require.NoError(t, err)
	assert.Equal(t, update.TimerActionSkipped, res.Action)
	assert.Contains(t, res.Reason, "automatic update work suppressed")
}

// ensureCountingRemote is a current remote that counts the timer heals
// `remote update` asks it for.
type ensureCountingRemote struct {
	ensureOnly int
	explicit   int
}

func (s *ensureCountingRemote) CheckBinary(context.Context) (string, bool) { return Version, true }
func (s *ensureCountingRemote) DetectPlatform(context.Context) (string, string, error) {
	return "", "", errors.New("stub: a current remote is never installed onto")
}
func (s *ensureCountingRemote) InstallBinary(context.Context, []byte, string) error { return nil }
func (s *ensureCountingRemote) FetchTimerStatus(context.Context) update.TimerStatus {
	return update.TimerStatus{Kind: update.TimerKindSystemd, Installed: true, Active: true}
}
func (s *ensureCountingRemote) InstallUpdateTimer(_ context.Context, ensureOnly bool) (session.RemoteTimerInstall, error) {
	if ensureOnly {
		s.ensureOnly++
	} else {
		s.explicit++
	}
	return session.RemoteTimerInstall{TimerEnsureResult: update.TimerEnsureResult{
		Action: update.TimerActionNone,
		Status: update.TimerStatus{Kind: update.TimerKindSystemd, Installed: true, Active: true},
	}}, nil
}

// `remote update <name>` asks each remote it leaves current or updated for
// its own timer heal (`update --ensure-timer`); a dry run never does.
func TestHandleRemoteUpdate_AsksTheRemoteToEnsureItsTimer(t *testing.T) {
	setupRemoteListJSONTest(t)
	stub := &ensureCountingRemote{}
	prev := remoteUpdateRunner
	remoteUpdateRunner = func(string, session.RemoteConfig) session.RemoteBinaryInstaller { return stub }
	t.Cleanup(func() { remoteUpdateRunner = prev })

	out := captureStdout(t, func() { handleRemoteUpdate([]string{"lab"}) })
	assert.Equal(t, 1, stub.ensureOnly, "remote update must ask the remote to ensure its timer; output:\n%s", out)
	assert.Equal(t, 0, stub.explicit, "the update path uses the automatic heal, not --install-timer")
	assert.Contains(t, out, "update timer")

	captureStdout(t, func() { handleRemoteUpdate([]string{"--dry-run", "lab"}) })
	assert.Equal(t, 1, stub.ensureOnly, "a dry run changes nothing on the remote")
}
