package update

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// disabledWebOutput is `launchctl print-disabled gui/501` on a Mac where
// the headless web daemon was turned off with `launchctl disable`.
const disabledWebOutput = "disabled services = {\n" +
	"\t\"com.apple.ScriptMenuApp\" => disabled\n" +
	"\t\"com.agentdeck.web\" => disabled\n" +
	"\t\"com.agentdeck.transition-notifier\" => enabled\n" +
	"}\n\n" +
	"login item associations = {\n" +
	"\t\"com.example.helper\" => disabled\n" +
	"}\n"

// What launchd answers, by design, to a bootstrap of a disabled label.
var disabledBootstrapEIO = fakeReply{out: "Bootstrap failed: 5: Input/output error", err: exitErr(5)}

const disabledWebLine = "  ⊘ com.agentdeck.web is disabled in launchd; left alone; `launchctl enable gui/501/com.agentdeck.web` to bring it back\n"

// #2457: com.agentdeck.web is on the gui domain's disabled list, so every
// bootstrap fails with EIO; the hygiene retried it after every update
// (158 attempts) and printed the same repair lines each time. A disabled
// agent is left alone: no bootout, no bootstrap, one line, nothing pending,
// and an entry an earlier run left for it is dropped.
func TestRebootstrapLaunchAgents_LeavesLaunchdDisabledAgentAlone(t *testing.T) {
	for _, tc := range []struct {
		name       string
		hadPending bool
	}{
		{"no earlier entry", false},
		{"earlier entry from the endless retry", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			exe, agents, plist := writeWebAgent(t)
			pending := pendingPath(t)
			clock := time.Date(2026, 10, 3, 1, 12, 0, 0, time.UTC)
			if tc.hadPending {
				cause := errors.New("`launchctl bootstrap gui/501 " + plist + "` failed (exit status 5): Bootstrap failed: 5: Input/output error")
				require.NoError(t, notePendingFailure(pending, "com.agentdeck.web", cause, clock.Add(-5*24*time.Hour)))
			}

			r := newFakeRunner()
			r.on("launchctl print-disabled gui/501", fakeReply{out: disabledWebOutput})
			r.on("launchctl bootstrap gui/501 "+plist, disabledBootstrapEIO)
			var out bytes.Buffer
			opts := pendingOpts(exe, agents, pending, r, func() time.Time { return clock })
			opts.Out = &out

			res, err := RebootstrapLaunchAgents(opts)
			require.NoError(t, err, "a disabled agent is not a failed update")
			assert.Equal(t, []string{"com.agentdeck.web"}, res.Disabled)
			assert.Empty(t, res.Restarted)
			assert.Equal(t, []string{"launchctl print-disabled gui/501"}, r.joined(), "no bootout, no bootstrap of a disabled agent")
			assert.Equal(t, disabledWebLine, out.String(), "exactly one line, no repair commands")

			agentsPending, err := PendingRebootstrapAgents(pending)
			require.NoError(t, err)
			assert.Empty(t, agentsPending)
			_, statErr := os.Stat(pending)
			assert.True(t, os.IsNotExist(statErr), "no marker left behind")
		})
	}
}

// The drain of a marker an earlier release left for a disabled agent drops
// the entry instead of retrying it forever.
func TestDrainPendingRebootstrap_DropsLaunchdDisabledAgent(t *testing.T) {
	exe, agents, plist := writeWebAgent(t)
	pending := pendingPath(t)
	clock := time.Date(2026, 10, 3, 1, 12, 0, 0, time.UTC)
	cause := errors.New("`launchctl bootstrap gui/501 " + plist + "` failed (exit status 5): Bootstrap failed: 5: Input/output error")
	for i := 0; i < 3; i++ {
		require.NoError(t, notePendingFailure(pending, "com.agentdeck.web", cause, clock))
	}

	r := newFakeRunner()
	r.on("launchctl print-disabled gui/501", fakeReply{out: disabledWebOutput})
	r.on("launchctl bootstrap gui/501 "+plist, disabledBootstrapEIO)
	var out bytes.Buffer
	opts := pendingOpts(exe, agents, pending, r, func() time.Time { return clock })
	opts.Out = &out

	res, err := DrainPendingRebootstrap(opts)
	require.NoError(t, err)
	assert.Equal(t, []string{"com.agentdeck.web"}, res.Disabled)
	assert.Empty(t, res.Restarted)
	assert.Equal(t, []string{"launchctl print-disabled gui/501"}, r.joined())
	assert.Equal(t, disabledWebLine, out.String())
	agentsPending, err := PendingRebootstrapAgents(pending)
	require.NoError(t, err)
	assert.Empty(t, agentsPending, "the pending entry is dropped")
}

// The disabled list is read once per run, however many agents there are,
// and only the disabled ones are left alone.
func TestRebootstrapLaunchAgents_ReadsDisabledListOncePerRun(t *testing.T) {
	home := t.TempDir()
	exe := filepath.Join(home, "agent-deck")
	require.NoError(t, os.WriteFile(exe, []byte("bin"), 0o755))
	agents := filepath.Join(home, "Library", "LaunchAgents")
	writeAgents(t, agents, exe)

	r := newFakeRunner()
	r.on("launchctl print-disabled gui/501", fakeReply{out: disabledWebOutput})
	r.on("launchctl print gui/501/com.agentdeck.transition-notifier", fakeReply{out: runningOutput("com.agentdeck.transition-notifier")})
	var out bytes.Buffer
	opts := pendingOpts(exe, agents, pendingPath(t), r, time.Now)
	opts.Out = &out

	res, err := RebootstrapLaunchAgents(opts)
	require.NoError(t, err)
	assert.Equal(t, []string{"com.agentdeck.transition-notifier"}, res.Restarted)
	assert.Equal(t, []string{"com.agentdeck.web"}, res.Disabled)
	assert.Equal(t, 1, countPrefix(r.joined(), "launchctl print-disabled"))
	assert.Equal(t, 0, countPrefix(r.joined(), "launchctl bootout gui/501/com.agentdeck.web"))
	assert.Equal(t, 0, countPrefix(r.joined(), "launchctl bootstrap gui/501 "+filepath.Join(agents, "com.agentdeck.web.plist")))
	assert.Equal(t, "  ↻ com.agentdeck.transition-notifier re-registered with launchd\n"+disabledWebLine, out.String())
}

// An enabled agent, or a print-disabled launchctl refuses, keeps the old
// path: same launchctl calls after the lookup, same single output line.
func TestRebootstrapLaunchAgents_EnabledAgentPathUnchanged(t *testing.T) {
	for name, reply := range map[string]fakeReply{
		"listed enabled":         {out: "disabled services = {\n\t\"com.agentdeck.web\" => enabled\n}\n"},
		"not listed":             {out: "disabled services = {\n}\n"},
		"print-disabled refused": {out: "Unrecognized subcommand: print-disabled", err: exitErr(64)},
	} {
		t.Run(name, func(t *testing.T) {
			exe, agents, plist := writeWebAgent(t)
			pending := pendingPath(t)
			r := newFakeRunner()
			r.on("launchctl print-disabled gui/501", reply)
			r.on("launchctl print gui/501/com.agentdeck.web", fakeReply{out: runningOutput("com.agentdeck.web")})
			var out bytes.Buffer
			opts := pendingOpts(exe, agents, pending, r, time.Now)
			opts.Out = &out

			res, err := RebootstrapLaunchAgents(opts)
			require.NoError(t, err)
			assert.Equal(t, []string{"com.agentdeck.web"}, res.Restarted)
			assert.Empty(t, res.Disabled)
			assert.Equal(t, []string{
				"launchctl print-disabled gui/501",
				"launchctl bootout gui/501/com.agentdeck.web",
				"launchctl bootstrap gui/501 " + plist,
				"launchctl print gui/501/com.agentdeck.web",
			}, r.joined())
			assert.Equal(t, "  ↻ com.agentdeck.web re-registered with launchd\n", out.String())
		})
	}
}

func TestParseDisabledLabels(t *testing.T) {
	assert.Equal(t, map[string]bool{"com.apple.ScriptMenuApp": true, "com.agentdeck.web": true}, parseDisabledLabels(disabledWebOutput),
		"only the disabled services block counts")
	legacy := "disabled services = {\n\t\"com.agentdeck.web\" => true\n\t\"com.agentdeck.transition-notifier\" => false\n}\n"
	assert.Equal(t, map[string]bool{"com.agentdeck.web": true}, parseDisabledLabels(legacy), "older macOS prints true/false")
	assert.Empty(t, parseDisabledLabels(""))
}

// `update --check --json` reports a pending agent launchd has disabled as
// disabled, not as an endless "bootstrap failed".
func TestMarkDisabledPending(t *testing.T) {
	since := time.Date(2026, 9, 28, 8, 25, 3, 0, time.UTC)
	in := []PendingAgent{
		{Label: "com.agentdeck.transition-notifier", Reason: PendingReasonInsideService, Since: since},
		{Label: "com.agentdeck.web", Reason: PendingReasonBootstrapFailed, Since: since, Attempts: 158, LastError: "Bootstrap failed: 5: Input/output error"},
	}
	opts := func(goos string, r *fakeRunner) RebootstrapOptions {
		return RebootstrapOptions{GOOS: goos, ExePath: "/x", LaunchAgentsDir: "/none", UID: 501, Runner: r, Logger: discardLogger(), PendingPath: pendingPath(t)}
	}

	r := newFakeRunner()
	r.on("launchctl print-disabled gui/501", fakeReply{out: disabledWebOutput})
	got := MarkDisabledPending(append([]PendingAgent(nil), in...), opts("darwin", r))
	require.Len(t, got, 2)
	assert.Equal(t, in[0], got[0], "an enabled agent is reported as before")
	assert.Equal(t, PendingAgent{Label: "com.agentdeck.web", Reason: PendingReasonDisabled, Since: since, Attempts: 158, LastError: "Bootstrap failed: 5: Input/output error", Disabled: true}, got[1])
	assert.Equal(t, []string{"launchctl print-disabled gui/501"}, r.joined())

	r2 := newFakeRunner()
	assert.Empty(t, MarkDisabledPending(nil, opts("darwin", r2)))
	assert.Empty(t, r2.calls, "nothing pending: launchctl is not asked")

	r3 := newFakeRunner()
	assert.Equal(t, in, MarkDisabledPending(append([]PendingAgent(nil), in...), opts("linux", r3)))
	assert.Empty(t, r3.calls)
}

func TestDescribePendingAgent_Disabled(t *testing.T) {
	since := time.Date(2026, 9, 28, 8, 25, 3, 0, time.UTC)
	assert.Equal(t, "com.agentdeck.web: disabled in launchd, left alone; the next update run drops it from this list",
		DescribePendingAgent(PendingAgent{Label: "com.agentdeck.web", Reason: PendingReasonDisabled, Since: since, Attempts: 158, Disabled: true}))
}
