package update

import (
	"io"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// pendingOpts are options for a run "in a fresh process": a new fake
// runner, the shared marker path and clock, nothing else carried over.
func pendingOpts(exe, agents, pending string, r *fakeRunner, now func() time.Time) RebootstrapOptions {
	return RebootstrapOptions{
		GOOS: "darwin", ExePath: exe, LaunchAgentsDir: agents, UID: 501,
		Runner: r, Sleep: func(time.Duration) {}, Out: io.Discard, Logger: discardLogger(),
		PendingPath: pending, now: now,
	}
}

// A bootstrap launchd never accepts after a successful bootout leaves the
// agent unloaded. The run still fails loudly, but the label is now
// remembered in the pending marker (which service, since when, how many
// attempts, the last error) so the next run, in a new process, retries
// it, and a run that finally gets it back clears the marker.
func TestRebootstrapLaunchAgents_HardBootstrapFailureIsRetriedByTheNextRun(t *testing.T) {
	exe, agents, plist := writeWebAgent(t)
	pending := pendingPath(t)
	eio := fakeReply{out: "Bootstrap failed: 5: Input/output error", err: exitErr(5)}
	clock := time.Date(2026, 9, 19, 14, 35, 0, 0, time.UTC)
	now := func() time.Time { return clock }

	// Run 1: the install's hygiene; seven EIO, hard failure.
	r1 := newFakeRunner()
	r1.on("launchctl bootstrap gui/501 "+plist, eio)
	_, err := RebootstrapLaunchAgents(pendingOpts(exe, agents, pending, r1, now))
	require.Error(t, err)
	assert.True(t, IsAgentRestartError(err))

	agentsPending, err := PendingRebootstrapAgents(pending)
	require.NoError(t, err)
	require.Len(t, agentsPending, 1)
	p := agentsPending[0]
	assert.Equal(t, "com.agentdeck.web", p.Label)
	assert.Equal(t, PendingReasonBootstrapFailed, p.Reason)
	assert.Equal(t, 1, p.Attempts)
	assert.True(t, p.Since.Equal(clock), "since = %v, want %v", p.Since, clock)
	assert.Contains(t, p.LastError, "Input/output error")
	labels, err := PendingRebootstrap(pending)
	require.NoError(t, err)
	assert.Equal(t, []string{"com.agentdeck.web"}, labels, "older readers still see the label")

	// Run 2, an hour later in a new process: the drain retries and fails
	// again; attempts grow, since is kept.
	clock = clock.Add(time.Hour)
	r2 := newFakeRunner()
	r2.on("launchctl bootstrap gui/501 "+plist, eio)
	_, err = DrainPendingRebootstrap(pendingOpts(exe, agents, pending, r2, now))
	require.Error(t, err)
	assert.Equal(t, bootstrapAttempts, countPrefix(r2.joined(), "launchctl bootstrap"), "the drain retried the bootstrap")
	agentsPending, err = PendingRebootstrapAgents(pending)
	require.NoError(t, err)
	require.Len(t, agentsPending, 1)
	assert.Equal(t, 2, agentsPending[0].Attempts)
	assert.True(t, agentsPending[0].Since.Equal(clock.Add(-time.Hour)), "since keeps the first failure")

	// Run 3: launchd accepts it; the marker is cleared.
	clock = clock.Add(time.Hour)
	r3 := newFakeRunner()
	r3.on("launchctl print gui/501/com.agentdeck.web", fakeReply{out: runningOutput("com.agentdeck.web")})
	res, err := DrainPendingRebootstrap(pendingOpts(exe, agents, pending, r3, now))
	require.NoError(t, err)
	assert.Equal(t, []string{"com.agentdeck.web"}, res.Restarted)
	agentsPending, err = PendingRebootstrapAgents(pending)
	require.NoError(t, err)
	assert.Empty(t, agentsPending)
	_, statErr := os.Stat(pending)
	assert.True(t, os.IsNotExist(statErr), "marker file removed once nothing is pending")
}

// A bootout that launchd refuses leaves the old instance registered and
// running; nothing is pending, the error alone is the signal.
func TestRebootstrapLaunchAgents_BootoutFailureIsNotPending(t *testing.T) {
	exe, agents, _ := writeWebAgent(t)
	pending := pendingPath(t)
	r := newFakeRunner()
	r.on("launchctl bootout gui/501/com.agentdeck.web", fakeReply{out: "Boot-out failed: 1: Operation not permitted", err: exitErr(1)})
	_, err := RebootstrapLaunchAgents(pendingOpts(exe, agents, pending, r, time.Now))
	require.Error(t, err)
	agentsPending, err := PendingRebootstrapAgents(pending)
	require.NoError(t, err)
	assert.Empty(t, agentsPending)
}

// `launchctl print` failing right after a bootstrap that launchd accepted
// is one more round from the plist (launchd may still be settling), not
// a hard stop; a second miss is remembered like any failure after bootout.
func TestRebootstrapLaunchAgents_VerifyFailureGetsASecondRoundThenPends(t *testing.T) {
	exe, agents, _ := writeWebAgent(t)
	pending := pendingPath(t)
	printFail := fakeReply{out: "Could not find service \"com.agentdeck.web\" in domain for user gui: 501", err: exitErr(113)}

	r := newFakeRunner()
	r.on("launchctl print gui/501/com.agentdeck.web", printFail, fakeReply{out: runningOutput("com.agentdeck.web")})
	res, err := RebootstrapLaunchAgents(pendingOpts(exe, agents, pending, r, time.Now))
	require.NoError(t, err)
	assert.Equal(t, []string{"com.agentdeck.web"}, res.Restarted)
	assert.Equal(t, 2, countPrefix(r.joined(), "launchctl bootstrap"), "second round from the plist")
	assert.Equal(t, 2, countPrefix(r.joined(), "launchctl bootout"))

	r2 := newFakeRunner()
	r2.on("launchctl print gui/501/com.agentdeck.web", printFail)
	_, err = RebootstrapLaunchAgents(pendingOpts(exe, agents, pending, r2, time.Now))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "failed after bootstrap")
	assert.Equal(t, 2, countPrefix(r2.joined(), "launchctl bootstrap"), "exactly one extra round")
	agentsPending, err := PendingRebootstrapAgents(pending)
	require.NoError(t, err)
	require.Len(t, agentsPending, 1)
	assert.Equal(t, PendingReasonBootstrapFailed, agentsPending[0].Reason)
	assert.Equal(t, 1, agentsPending[0].Attempts)
}

// The own-service deferral records why and when, with no attempt yet.
func TestDeferOwnService_RecordsReasonAndSince(t *testing.T) {
	exe, agents, _ := writeWebAgent(t)
	pending := pendingPath(t)
	clock := time.Date(2026, 9, 19, 9, 0, 0, 0, time.UTC)
	opts := pendingOpts(exe, agents, pending, newFakeRunner(), func() time.Time { return clock })
	opts.ServiceLabel = "com.agentdeck.web"
	res, err := RebootstrapLaunchAgents(opts)
	require.NoError(t, err)
	assert.Equal(t, []string{"com.agentdeck.web"}, res.Deferred)
	agentsPending, err := PendingRebootstrapAgents(pending)
	require.NoError(t, err)
	require.Len(t, agentsPending, 1)
	assert.Equal(t, PendingAgent{Label: "com.agentdeck.web", Reason: PendingReasonInsideService, Since: clock}, agentsPending[0])
}

// A marker written by the previous release (labels only) still reads as
// pending agents, with the file's update time as since.
func TestPendingRebootstrapAgents_ReadsLabelsOnlyMarker(t *testing.T) {
	pending := pendingPath(t)
	require.NoError(t, os.MkdirAll(filepath.Dir(pending), 0o755))
	require.NoError(t, os.WriteFile(pending, []byte(`{"labels":["com.agentdeck.web"],"updated_at":"2026-09-19T08:00:00Z"}`), 0o644))
	agentsPending, err := PendingRebootstrapAgents(pending)
	require.NoError(t, err)
	require.Len(t, agentsPending, 1)
	assert.Equal(t, "com.agentdeck.web", agentsPending[0].Label)
	assert.Equal(t, time.Date(2026, 9, 19, 8, 0, 0, 0, time.UTC), agentsPending[0].Since)
	assert.Equal(t, "", agentsPending[0].Reason, "unknown reason stays unknown")
}

func TestDescribePendingAgent(t *testing.T) {
	now := time.Date(2026, 9, 19, 12, 0, 0, 0, time.UTC)
	since := now.Add(-90 * time.Minute)
	assert.Equal(t, "com.agentdeck.web: bootstrap failed 2 times since 10:30 (last: Input/output error); every update run retries it",
		DescribePendingAgent(PendingAgent{Label: "com.agentdeck.web", Reason: PendingReasonBootstrapFailed, Since: since, Attempts: 2, LastError: "Input/output error"}))
	assert.Equal(t, "com.agentdeck.web: deferred since 10:30, the updater ran inside it; the next update run outside it re-registers it",
		DescribePendingAgent(PendingAgent{Label: "com.agentdeck.web", Reason: PendingReasonInsideService, Since: since}))
	assert.Equal(t, "com.agentdeck.web: pending since 10:30",
		DescribePendingAgent(PendingAgent{Label: "com.agentdeck.web", Since: since}))
}
