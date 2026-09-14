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

// fakeExit mimics *exec.ExitError for the fake runner.
type fakeExit struct{ code int }

func (e fakeExit) Error() string  { return fmt.Sprintf("exit status %d", e.code) }
func (e fakeExit) ExitCode() int  { return e.code }
func exitErr(code int) error      { return fakeExit{code: code} }
func discardLogger() *slog.Logger { return slog.New(slog.NewTextHandler(io.Discard, nil)) }

// fakeRunner records every argv and answers from a queue keyed by the joined
// command; a missing key answers success with empty output.
type fakeRunner struct {
	calls   [][]string
	replies map[string][]fakeReply
}

type fakeReply struct {
	out string
	err error
}

func newFakeRunner() *fakeRunner { return &fakeRunner{replies: map[string][]fakeReply{}} }

func (f *fakeRunner) on(argv string, replies ...fakeReply) {
	f.replies[argv] = append(f.replies[argv], replies...)
}

func (f *fakeRunner) Run(argv ...string) (string, error) {
	f.calls = append(f.calls, append([]string(nil), argv...))
	key := strings.Join(argv, " ")
	q := f.replies[key]
	if len(q) == 0 {
		return "", nil
	}
	r := q[0]
	if len(q) > 1 {
		f.replies[key] = q[1:]
	}
	return r.out, r.err
}

func (f *fakeRunner) joined() []string {
	out := make([]string, len(f.calls))
	for i, c := range f.calls {
		out[i] = strings.Join(c, " ")
	}
	return out
}

const notifierPlist = `<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0">
<dict>
    <key>Label</key>
    <string>com.agentdeck.transition-notifier</string>
    <key>ProgramArguments</key>
    <array>
        <string>%s</string>
        <string>notify-daemon</string>
    </array>
    <key>RunAtLoad</key>
    <true/>
    <key>KeepAlive</key>
    <true/>
    <key>StandardOutPath</key>
    <string>/tmp/x/transition-notifier.log</string>
    <key>EnvironmentVariables</key>
    <dict>
        <key>PATH</key>
        <string>/usr/bin:/bin</string>
    </dict>
    <key>ThrottleInterval</key>
    <integer>5</integer>
</dict>
</plist>
`

const webPlist = `<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN"
  "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0">
<dict>
  <key>Label</key>
  <string>com.agentdeck.web</string>
  <key>ProgramArguments</key>
  <array>
    <string>%s</string>
    <string>-p</string>
    <string>personal</string>
    <string>web</string>
    <string>--no-tui</string>
  </array>
  <key>RunAtLoad</key>
  <true/>
  <key>KeepAlive</key>
  <dict>
    <key>SuccessfulExit</key>
    <false/>
  </dict>
</dict>
</plist>
`

const pythonPlist = `<?xml version="1.0" encoding="UTF-8"?>
<plist version="1.0">
<dict>
  <key>Label</key>
  <string>com.agentdeck.reddit-monitor</string>
  <key>Program</key>
  <string>/usr/bin/python3</string>
  <key>ProgramArguments</key>
  <array>
    <string>/usr/bin/python3</string>
    <string>/opt/monitor.py</string>
  </array>
  <key>StartInterval</key>
  <integer>600</integer>
</dict>
</plist>
`

const foreignPlist = `<?xml version="1.0" encoding="UTF-8"?>
<plist version="1.0">
<dict>
  <key>Label</key>
  <string>com.example.other</string>
  <key>ProgramArguments</key>
  <array>
    <string>%s</string>
  </array>
  <key>KeepAlive</key>
  <true/>
</dict>
</plist>
`

func TestParseLaunchAgentPlist(t *testing.T) {
	exe := "/Users/me/.local/bin/agent-deck"

	a, err := ParseLaunchAgentPlist([]byte(fmt.Sprintf(notifierPlist, exe)))
	require.NoError(t, err)
	assert.Equal(t, "com.agentdeck.transition-notifier", a.Label)
	assert.Equal(t, []string{exe, "notify-daemon"}, a.ProgramArguments)
	assert.Equal(t, exe, a.ProgramPath())
	assert.True(t, a.KeepAlive)
	assert.True(t, a.RunAtLoad)

	w, err := ParseLaunchAgentPlist([]byte(fmt.Sprintf(webPlist, exe)))
	require.NoError(t, err)
	assert.Equal(t, "com.agentdeck.web", w.Label)
	assert.Equal(t, exe, w.ProgramPath())
	assert.True(t, w.KeepAlive, "dict-form KeepAlive counts as keep-alive")
	assert.True(t, w.RunAtLoad)

	p, err := ParseLaunchAgentPlist([]byte(pythonPlist))
	require.NoError(t, err)
	assert.Equal(t, "/usr/bin/python3", p.ProgramPath())
	assert.False(t, p.KeepAlive)
	assert.False(t, p.RunAtLoad)

	timer := TimerConfig{Exe: exe, Home: "/Users/me", LogDir: "/Users/me/.agent-deck/logs", Minute: 5}
	tp, err := ParseLaunchAgentPlist(timer.LaunchdPlist())
	require.NoError(t, err)
	assert.Equal(t, AutoupdateLabel, tp.Label)
	assert.Equal(t, "/bin/sh", tp.ProgramPath())
	assert.False(t, tp.RunAtLoad)

	_, err = ParseLaunchAgentPlist([]byte(`<plist version="1.0"><dict><key>Program</key><string>/x</string></dict></plist>`))
	assert.ErrorContains(t, err, "no Label")
	_, err = ParseLaunchAgentPlist([]byte(`not xml at all`))
	assert.Error(t, err)
}

// writeAgents lays out a LaunchAgents dir with the four fixture shapes.
func writeAgents(t *testing.T, dir, exe string) {
	t.Helper()
	require.NoError(t, os.MkdirAll(dir, 0o755))
	write := func(name, content string) {
		require.NoError(t, os.WriteFile(filepath.Join(dir, name), []byte(content), 0o644))
	}
	write("com.agentdeck.transition-notifier.plist", fmt.Sprintf(notifierPlist, exe))
	write("com.agentdeck.web.plist", fmt.Sprintf(webPlist, exe))
	write("com.agentdeck.reddit-monitor.plist", pythonPlist)
	write("com.example.other.plist", fmt.Sprintf(foreignPlist, exe))
	timer := TimerConfig{Exe: exe, Home: "/Users/me", LogDir: "/tmp/logs", Minute: 7}
	write(LaunchdTimerPlistName, string(timer.LaunchdPlist()))
	write("garbage.plist", "<<not a plist")
}

func runningOutput(label string) string {
	return fmt.Sprintf("gui/501/%s = {\n\tactive count = 1\n\tstate = running\n\n\tprogram = /x\n}\n", label)
}

func waitingOutput(label string) string {
	return fmt.Sprintf("gui/501/%s = {\n\tactive count = 0\n\tstate = waiting\n}\n", label)
}

func TestRebootstrapLaunchAgents_SelectsOnlyOurBinary(t *testing.T) {
	home := t.TempDir()
	binDir := filepath.Join(home, ".local", "bin")
	require.NoError(t, os.MkdirAll(binDir, 0o755))
	exe := filepath.Join(binDir, "agent-deck")
	require.NoError(t, os.WriteFile(exe, []byte("#!/bin/sh\n"), 0o755))
	agents := filepath.Join(home, "Library", "LaunchAgents")
	writeAgents(t, agents, exe)

	r := newFakeRunner()
	r.on("launchctl bootout gui/501/com.agentdeck.transition-notifier", fakeReply{out: "Boot-out failed: 3: No such process", err: exitErr(3)})
	r.on("launchctl print gui/501/com.agentdeck.transition-notifier", fakeReply{out: runningOutput("com.agentdeck.transition-notifier")})
	r.on("launchctl print gui/501/com.agentdeck.web",
		fakeReply{out: waitingOutput("com.agentdeck.web")},
		fakeReply{out: runningOutput("com.agentdeck.web")},
	)

	var slept []time.Duration
	var out bytes.Buffer
	res, err := RebootstrapLaunchAgents(RebootstrapOptions{
		GOOS: "darwin", ExePath: exe, LaunchAgentsDir: agents, UID: 501,
		Runner: r, Sleep: func(d time.Duration) { slept = append(slept, d) },
		Out: &out, Logger: discardLogger(),
	})
	require.NoError(t, err)

	assert.Equal(t, []string{"com.agentdeck.transition-notifier", "com.agentdeck.web"}, res.Restarted)
	assert.Contains(t, res.Skipped, AutoupdateLabel)
	assert.Contains(t, res.Skipped["com.agentdeck.reddit-monitor"], "/usr/bin/python3")
	assert.NotContains(t, res.Skipped, "com.example.other", "foreign labels are ignored entirely")

	assert.Equal(t, []string{
		"launchctl bootout gui/501/com.agentdeck.transition-notifier",
		"launchctl bootstrap gui/501 " + filepath.Join(agents, "com.agentdeck.transition-notifier.plist"),
		"launchctl print gui/501/com.agentdeck.transition-notifier",
		"launchctl bootout gui/501/com.agentdeck.web",
		"launchctl bootstrap gui/501 " + filepath.Join(agents, "com.agentdeck.web.plist"),
		"launchctl print gui/501/com.agentdeck.web",
		"launchctl print gui/501/com.agentdeck.web",
	}, r.joined())
	assert.Equal(t, []time.Duration{500 * time.Millisecond}, slept, "one poll step while web was still waiting")
	assert.Contains(t, out.String(), "↻ com.agentdeck.transition-notifier re-registered with launchd")
	assert.Contains(t, out.String(), "↻ com.agentdeck.web re-registered with launchd")
}

func TestRebootstrapLaunchAgents_MatchesThroughSymlink(t *testing.T) {
	home := t.TempDir()
	real := filepath.Join(home, "releases", "agent-deck-1.2.3")
	require.NoError(t, os.MkdirAll(filepath.Dir(real), 0o755))
	require.NoError(t, os.WriteFile(real, []byte("bin"), 0o755))
	link := filepath.Join(home, "bin", "agent-deck")
	require.NoError(t, os.MkdirAll(filepath.Dir(link), 0o755))
	require.NoError(t, os.Symlink(real, link))

	agents := filepath.Join(home, "Library", "LaunchAgents")
	require.NoError(t, os.MkdirAll(agents, 0o755))
	// The plist names the symlink; the updater resolved the real file.
	require.NoError(t, os.WriteFile(filepath.Join(agents, "com.agentdeck.web.plist"), []byte(fmt.Sprintf(webPlist, link)), 0o644))

	r := newFakeRunner()
	r.on("launchctl print gui/7/com.agentdeck.web", fakeReply{out: runningOutput("com.agentdeck.web")})
	res, err := RebootstrapLaunchAgents(RebootstrapOptions{
		GOOS: "darwin", ExePath: real, LaunchAgentsDir: agents, UID: 7,
		Runner: r, Sleep: func(time.Duration) {}, Out: io.Discard, Logger: discardLogger(),
	})
	require.NoError(t, err)
	assert.Equal(t, []string{"com.agentdeck.web"}, res.Restarted)
	assert.Equal(t, "launchctl bootout gui/7/com.agentdeck.web", r.joined()[0])
}

func TestRebootstrapLaunchAgents_FailsWhenAgentNeverRuns(t *testing.T) {
	home := t.TempDir()
	exe := filepath.Join(home, "agent-deck")
	require.NoError(t, os.WriteFile(exe, []byte("bin"), 0o755))
	agents := filepath.Join(home, "LaunchAgents")
	require.NoError(t, os.MkdirAll(agents, 0o755))
	plist := filepath.Join(agents, "com.agentdeck.transition-notifier.plist")
	require.NoError(t, os.WriteFile(plist, []byte(fmt.Sprintf(notifierPlist, exe)), 0o644))

	r := newFakeRunner()
	r.on("launchctl print gui/501/com.agentdeck.transition-notifier", fakeReply{out: waitingOutput("com.agentdeck.transition-notifier")})

	clock := time.Date(2026, 7, 30, 7, 0, 0, 0, time.UTC)
	var polls int
	var out bytes.Buffer
	_, err := RebootstrapLaunchAgents(RebootstrapOptions{
		GOOS: "darwin", ExePath: exe, LaunchAgentsDir: agents, UID: 501,
		Runner: r, Sleep: func(d time.Duration) { polls++; clock = clock.Add(d) },
		Out: &out, Logger: discardLogger(),
		now: func() time.Time { return clock },
	})
	require.Error(t, err)
	assert.True(t, IsAgentRestartError(err))
	assert.Contains(t, err.Error(), "com.agentdeck.transition-notifier did not come back")
	assert.Contains(t, err.Error(), "state = waiting")
	assert.Contains(t, err.Error(), "run: launchctl bootout gui/501/com.agentdeck.transition-notifier; launchctl bootstrap gui/501 "+plist)
	assert.Contains(t, out.String(), "✗ com.agentdeck.transition-notifier")
	assert.Equal(t, 20, polls, "10s budget in 500ms steps")
	assert.Len(t, r.calls, 3+20, "bootout, bootstrap, first print, then one print per poll")
}

func TestRebootstrapLaunchAgents_BootstrapRetriesThenFails(t *testing.T) {
	home := t.TempDir()
	exe := filepath.Join(home, "agent-deck")
	require.NoError(t, os.WriteFile(exe, []byte("bin"), 0o755))
	agents := filepath.Join(home, "LaunchAgents")
	require.NoError(t, os.MkdirAll(agents, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(agents, "com.agentdeck.web.plist"), []byte(fmt.Sprintf(webPlist, exe)), 0o644))

	r := newFakeRunner()
	r.on("launchctl bootstrap gui/501 "+filepath.Join(agents, "com.agentdeck.web.plist"),
		fakeReply{out: "Bootstrap failed: 5: Input/output error", err: exitErr(5)})

	_, err := RebootstrapLaunchAgents(RebootstrapOptions{
		GOOS: "darwin", ExePath: exe, LaunchAgentsDir: agents, UID: 501,
		Runner: r, Sleep: func(time.Duration) {}, Out: io.Discard, Logger: discardLogger(),
	})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "Input/output error")
	bootstraps := 0
	for _, c := range r.joined() {
		if strings.HasPrefix(c, "launchctl bootstrap") {
			bootstraps++
		}
	}
	assert.Equal(t, 5, bootstraps)
}

func TestRebootstrapLaunchAgents_BootoutHardFailureStops(t *testing.T) {
	home := t.TempDir()
	exe := filepath.Join(home, "agent-deck")
	require.NoError(t, os.WriteFile(exe, []byte("bin"), 0o755))
	agents := filepath.Join(home, "LaunchAgents")
	require.NoError(t, os.MkdirAll(agents, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(agents, "com.agentdeck.web.plist"), []byte(fmt.Sprintf(webPlist, exe)), 0o644))

	r := newFakeRunner()
	r.on("launchctl bootout gui/501/com.agentdeck.web", fakeReply{out: "Boot-out failed: 1: Operation not permitted", err: exitErr(1)})
	_, err := RebootstrapLaunchAgents(RebootstrapOptions{
		GOOS: "darwin", ExePath: exe, LaunchAgentsDir: agents, UID: 501,
		Runner: r, Sleep: func(time.Duration) {}, Out: io.Discard, Logger: discardLogger(),
	})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "Operation not permitted")
	assert.Len(t, r.calls, 1, "no bootstrap after a hard bootout failure")
}

func TestRebootstrapLaunchAgents_NoopOffDarwin(t *testing.T) {
	r := newFakeRunner()
	res, err := RebootstrapLaunchAgents(RebootstrapOptions{GOOS: "linux", ExePath: "/x", LaunchAgentsDir: t.TempDir(), UID: 1, Runner: r, Logger: discardLogger()})
	require.NoError(t, err)
	assert.Empty(t, res.Restarted)
	assert.Empty(t, r.calls)
	require.NoError(t, PreflightLaunchctl(RebootstrapOptions{GOOS: "linux", ExePath: "/x", LaunchAgentsDir: "/none", UID: 1, Runner: r}))
}

func TestPreflightLaunchctl(t *testing.T) {
	// launchctl is looked up on PATH; provide a fake so the test runs on any OS.
	bin := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(bin, "launchctl"), []byte("#!/bin/sh\nexit 0\n"), 0o755))
	t.Setenv("PATH", bin)

	r := newFakeRunner()
	require.NoError(t, PreflightLaunchctl(RebootstrapOptions{GOOS: "darwin", ExePath: "/x", LaunchAgentsDir: "/none", UID: 501, Runner: r, Logger: discardLogger()}))
	assert.Equal(t, []string{"launchctl print gui/501"}, r.joined())

	r2 := newFakeRunner()
	r2.on("launchctl print gui/501", fakeReply{out: "Could not find domain for gui/501", err: exitErr(1)})
	err := PreflightLaunchctl(RebootstrapOptions{GOOS: "darwin", ExePath: "/x", LaunchAgentsDir: "/none", UID: 501, Runner: r2, Logger: discardLogger()})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "launchctl print gui/501")
	assert.Contains(t, err.Error(), "Could not find domain")

	t.Setenv("PATH", t.TempDir())
	err = PreflightLaunchctl(RebootstrapOptions{GOOS: "darwin", ExePath: "/x", LaunchAgentsDir: "/none", UID: 501, Runner: r, Logger: discardLogger()})
	assert.ErrorContains(t, err, "launchctl not found")
}

func TestLaunchctlNotLoaded(t *testing.T) {
	assert.True(t, launchctlNotLoaded("Boot-out failed: 3: No such process", exitErr(3)))
	assert.True(t, launchctlNotLoaded("Could not find service \"x\" in domain for user gui: 501", exitErr(113)))
	assert.False(t, launchctlNotLoaded("Boot-out failed: 1: Operation not permitted", exitErr(1)))
}
