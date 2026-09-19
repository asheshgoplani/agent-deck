package update

import (
	"bytes"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// writeWebAgent writes a com.agentdeck.web plist whose program is exe and
// returns (exe, agents dir, plist path).
func writeWebAgent(t *testing.T) (string, string, string) {
	t.Helper()
	home := t.TempDir()
	exe := filepath.Join(home, "agent-deck")
	require.NoError(t, os.WriteFile(exe, []byte("bin"), 0o755))
	agents := filepath.Join(home, "LaunchAgents")
	require.NoError(t, os.MkdirAll(agents, 0o755))
	plist := filepath.Join(agents, "com.agentdeck.web.plist")
	require.NoError(t, os.WriteFile(plist, []byte(fmt.Sprintf(webPlist, exe)), 0o644))
	return exe, agents, plist
}

func countPrefix(calls []string, prefix string) int {
	n := 0
	for _, c := range calls {
		if strings.HasPrefix(c, prefix) {
			n++
		}
	}
	return n
}

// The 2026-09-19 incident: bootout returns before launchd has torn the old
// instance down, so the first bootstraps fail with EIO. The hygiene must
// keep trying with a growing pause until one succeeds, then verify.
func TestRebootstrapLaunchAgents_BootstrapBackoffUntilItSticks(t *testing.T) {
	exe, agents, plist := writeWebAgent(t)
	r := newFakeRunner()
	eio := fakeReply{out: "Bootstrap failed: 5: Input/output error", err: exitErr(5)}
	r.on("launchctl bootstrap gui/501 "+plist, eio, eio, eio, eio, eio, eio, fakeReply{})
	r.on("launchctl print gui/501/com.agentdeck.web", fakeReply{out: runningOutput("com.agentdeck.web")})

	var slept []time.Duration
	var out bytes.Buffer
	res, err := RebootstrapLaunchAgents(RebootstrapOptions{
		GOOS: "darwin", ExePath: exe, LaunchAgentsDir: agents, UID: 501,
		Runner: r, Sleep: func(d time.Duration) { slept = append(slept, d) },
		Out: &out, Logger: discardLogger(), PendingPath: pendingPath(t),
	})
	require.NoError(t, err)
	assert.Equal(t, []string{"com.agentdeck.web"}, res.Restarted)
	assert.Equal(t, 7, countPrefix(r.joined(), "launchctl bootstrap"), "six EIO failures then success")
	assert.Equal(t, []time.Duration{
		500 * time.Millisecond, time.Second, 2 * time.Second, 4 * time.Second, 8 * time.Second, 8 * time.Second,
	}, slept, "backoff doubles to the 8s cap")
}

// Registered but never running after the verify window: re-bootstrap from
// the plist once (bootout + bootstrap + verify again) before giving up.
func TestRebootstrapLaunchAgents_RebootstrapsFromPlistWhenNotRunning(t *testing.T) {
	exe, agents, plist := writeWebAgent(t)
	r := newFakeRunner()
	// First round: bootstrap ok, but the agent sits in "waiting" for the
	// whole window. Second round: running at once.
	r.on("launchctl print gui/501/com.agentdeck.web",
		fakeReply{out: waitingOutput("com.agentdeck.web")},
		fakeReply{out: waitingOutput("com.agentdeck.web")},
		fakeReply{out: waitingOutput("com.agentdeck.web")},
		fakeReply{out: runningOutput("com.agentdeck.web")},
	)
	clock := time.Date(2026, 9, 19, 14, 35, 0, 0, time.UTC)
	var logBuf bytes.Buffer
	log := newTextLogger(&logBuf)
	res, err := RebootstrapLaunchAgents(RebootstrapOptions{
		GOOS: "darwin", ExePath: exe, LaunchAgentsDir: agents, UID: 501,
		Runner: r, Sleep: func(d time.Duration) { clock = clock.Add(d) },
		Out: io.Discard, Logger: log, VerifyTimeout: time.Second, PendingPath: pendingPath(t),
		now: func() time.Time { return clock },
	})
	require.NoError(t, err)
	assert.Equal(t, []string{"com.agentdeck.web"}, res.Restarted)
	assert.Equal(t, 2, countPrefix(r.joined(), "launchctl bootout"), "second round boots out again")
	assert.Equal(t, 2, countPrefix(r.joined(), "launchctl bootstrap"))
	assert.Contains(t, logBuf.String(), "launchagent_rebootstrap_from_plist")
	assert.Contains(t, logBuf.String(), "launchctl bootout gui/501/com.agentdeck.web; launchctl bootstrap gui/501 "+plist)
	assert.Contains(t, logBuf.String(), "level=WARN")
}

// Still not running after the second round: the hygiene fails, loudly, with
// the repair commands, so the unattended run exits 1 instead of pretending.
func TestRebootstrapLaunchAgents_FailsAfterSecondRound(t *testing.T) {
	exe, agents, plist := writeWebAgent(t)
	r := newFakeRunner()
	r.on("launchctl print gui/501/com.agentdeck.web", fakeReply{out: waitingOutput("com.agentdeck.web")})
	clock := time.Now()
	var out bytes.Buffer
	_, err := RebootstrapLaunchAgents(RebootstrapOptions{
		GOOS: "darwin", ExePath: exe, LaunchAgentsDir: agents, UID: 501,
		Runner: r, Sleep: func(d time.Duration) { clock = clock.Add(d) },
		Out: &out, Logger: discardLogger(), VerifyTimeout: time.Second, PendingPath: pendingPath(t),
		now: func() time.Time { return clock },
	})
	require.Error(t, err)
	assert.True(t, IsAgentRestartError(err))
	assert.Contains(t, err.Error(), "run: launchctl bootout gui/501/com.agentdeck.web; launchctl bootstrap gui/501 "+plist)
	assert.Equal(t, 2, countPrefix(r.joined(), "launchctl bootstrap"), "exactly one re-bootstrap round")
	assert.Contains(t, out.String(), "✗ com.agentdeck.web")
}

// The updater that runs as a child of com.agentdeck.web (the headless
// daemon's Installer) must never bootout its own service: launchd kills the
// whole service, updater included, before it can bootstrap again (2026-09-19:
// com.agentdeck.web absent until re-bootstrapped by hand, update.lock left
// behind, no remote sweep). It defers that one label to a pending marker
// the next run outside the service drains.
func TestRebootstrapLaunchAgents_DefersOwnService(t *testing.T) {
	exe, agents, plist := writeWebAgent(t)
	require.NoError(t, os.WriteFile(filepath.Join(agents, "com.agentdeck.transition-notifier.plist"), []byte(fmt.Sprintf(notifierPlist, exe)), 0o644))
	r := newFakeRunner()
	r.on("launchctl print gui/501/com.agentdeck.transition-notifier", fakeReply{out: runningOutput("com.agentdeck.transition-notifier")})
	pending := filepath.Join(t.TempDir(), "pending.json")
	var logBuf bytes.Buffer
	var out bytes.Buffer
	res, err := RebootstrapLaunchAgents(RebootstrapOptions{
		GOOS: "darwin", ExePath: exe, LaunchAgentsDir: agents, UID: 501,
		Runner: r, Sleep: func(time.Duration) {}, Out: &out, Logger: newTextLogger(&logBuf),
		ServiceLabel: "com.agentdeck.web", PendingPath: pending,
	})
	require.NoError(t, err)
	assert.Equal(t, []string{"com.agentdeck.transition-notifier"}, res.Restarted)
	assert.Equal(t, []string{"com.agentdeck.web"}, res.Deferred)
	assert.Equal(t, 0, countPrefix(r.joined(), "launchctl bootout gui/501/com.agentdeck.web"), "never boots itself out")
	assert.Contains(t, logBuf.String(), "launchagent_self_deferred")
	assert.Contains(t, logBuf.String(), "launchctl bootout gui/501/com.agentdeck.web; launchctl bootstrap gui/501 "+plist)
	assert.Contains(t, out.String(), "com.agentdeck.web: deferred")

	labels, err := PendingRebootstrap(pending)
	require.NoError(t, err)
	assert.Equal(t, []string{"com.agentdeck.web"}, labels)

	// A later run outside the service drains the marker: only the pending
	// label is touched, and the marker is cleared once it is running.
	r2 := newFakeRunner()
	r2.on("launchctl print gui/501/com.agentdeck.web", fakeReply{out: runningOutput("com.agentdeck.web")})
	res2, err := DrainPendingRebootstrap(RebootstrapOptions{
		GOOS: "darwin", ExePath: exe, LaunchAgentsDir: agents, UID: 501,
		Runner: r2, Sleep: func(time.Duration) {}, Out: io.Discard, Logger: discardLogger(),
		ServiceLabel: "", PendingPath: pending,
	})
	require.NoError(t, err)
	assert.Equal(t, []string{"com.agentdeck.web"}, res2.Restarted)
	assert.Equal(t, []string{
		"launchctl bootout gui/501/com.agentdeck.web",
		"launchctl bootstrap gui/501 " + plist,
		"launchctl print gui/501/com.agentdeck.web",
	}, r2.joined())
	labels, err = PendingRebootstrap(pending)
	require.NoError(t, err)
	assert.Empty(t, labels, "marker cleared")
}

// Draining from inside the very service that is pending keeps it pending.
func TestDrainPendingRebootstrap_SkipsOwnService(t *testing.T) {
	exe, agents, _ := writeWebAgent(t)
	pending := filepath.Join(t.TempDir(), "pending.json")
	require.NoError(t, addPendingRebootstrap(pending, "com.agentdeck.web"))
	r := newFakeRunner()
	res, err := DrainPendingRebootstrap(RebootstrapOptions{
		GOOS: "darwin", ExePath: exe, LaunchAgentsDir: agents, UID: 501,
		Runner: r, Sleep: func(time.Duration) {}, Out: io.Discard, Logger: discardLogger(),
		ServiceLabel: "com.agentdeck.web", PendingPath: pending,
	})
	require.NoError(t, err)
	assert.Empty(t, res.Restarted)
	assert.Equal(t, []string{"com.agentdeck.web"}, res.Deferred)
	assert.Empty(t, r.calls)
	labels, _ := PendingRebootstrap(pending)
	assert.Equal(t, []string{"com.agentdeck.web"}, labels)
}

func TestInsideLaunchdService(t *testing.T) {
	assert.True(t, insideLaunchdService("com.agentdeck.web", "com.agentdeck.web"))
	assert.False(t, insideLaunchdService("", "com.agentdeck.web"))
	assert.False(t, insideLaunchdService("com.agentdeck.transition-notifier", "com.agentdeck.web"))
}

func newTextLogger(w io.Writer) *slog.Logger {
	return slog.New(slog.NewTextHandler(w, &slog.HandlerOptions{Level: slog.LevelDebug}))
}
