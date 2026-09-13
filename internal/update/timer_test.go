package update

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func darwinTimerConfig(t *testing.T) TimerConfig {
	t.Helper()
	home := t.TempDir()
	return TimerConfig{
		GOOS:            "darwin",
		Exe:             "/Users/me/.local/bin/agent-deck",
		UID:             501,
		Home:            home,
		LogDir:          filepath.Join(home, ".agent-deck", "logs"),
		LaunchAgentsDir: filepath.Join(home, "Library", "LaunchAgents"),
		SystemdUserDir:  filepath.Join(home, ".config", "systemd", "user"),
		Minute:          23,
	}
}

func linuxTimerConfig(t *testing.T) TimerConfig {
	t.Helper()
	c := darwinTimerConfig(t)
	c.GOOS = "linux"
	c.Exe = "/home/me/.local/bin/agent-deck"
	c.UID = 1000
	return c
}

func TestLaunchdPlist_ShapeAndIdentity(t *testing.T) {
	c := darwinTimerConfig(t)
	c.Exe = "/Users/me/my apps/agent-deck"
	data := c.LaunchdPlist()
	s := string(data)

	agent, err := ParseLaunchAgentPlist(data)
	require.NoError(t, err)
	assert.Equal(t, AutoupdateLabel, agent.Label)
	// /bin/sh keeps the BTM identity off the agent-deck binary; $0 carries the
	// real path so the plist never embeds it in a shell string.
	assert.Equal(t, []string{"/bin/sh", "-c", `exec "$0" update --unattended --trigger timer`, c.Exe}, agent.ProgramArguments)
	assert.False(t, agent.RunAtLoad)
	assert.False(t, agent.KeepAlive)

	assert.Contains(t, s, "<key>Hour</key>\n        <integer>7</integer>")
	assert.Contains(t, s, "<key>Minute</key>\n        <integer>23</integer>")
	assert.Contains(t, s, "<string>"+filepath.Join(c.LogDir, TimerLogFileName)+"</string>")
	assert.Contains(t, s, "<key>PATH</key>\n        <string>/Users/me/my apps:/usr/local/bin:/opt/homebrew/bin:/usr/bin:/bin:/usr/sbin:/sbin</string>")
	assert.Contains(t, s, "<key>HOME</key>\n        <string>"+c.Home+"</string>")
	assert.Contains(t, s, "<key>AGENTDECK_UPDATE_TRIGGER</key>\n        <string>timer</string>")
	assert.Equal(t, "daily at 07:23 local time", launchdScheduleDetail(data))
}

func TestSystemdUnits(t *testing.T) {
	c := linuxTimerConfig(t)
	svc := string(c.SystemdService())
	assert.Contains(t, svc, "Type=oneshot")
	assert.Contains(t, svc, "Environment=AGENTDECK_UPDATE_TRIGGER=timer")
	assert.Contains(t, svc, "ExecStart=/home/me/.local/bin/agent-deck update --unattended --trigger timer")

	c.Exe = "/home/me/my apps/agent-deck"
	assert.Contains(t, string(c.SystemdService()), `ExecStart="/home/me/my apps/agent-deck" update`)

	timer := string(c.SystemdTimer())
	assert.Contains(t, timer, "OnCalendar=daily")
	assert.Contains(t, timer, "RandomizedDelaySec=1h")
	assert.Contains(t, timer, "Persistent=true")
	assert.Contains(t, timer, "WantedBy=timers.target")
}

func TestInstallTimerPlan_Darwin(t *testing.T) {
	c := darwinTimerConfig(t)
	plan, err := InstallTimerPlan(c)
	require.NoError(t, err)

	r := newFakeRunner()
	// Existing timer not loaded: bootout's "not found" is tolerated.
	r.on("launchctl bootout gui/501/"+AutoupdateLabel, fakeReply{out: "Boot-out failed: 3: No such process", err: exitErr(3)})
	require.NoError(t, plan.Execute(r, discardLogger()))

	assert.Equal(t, []string{
		"launchctl bootout gui/501/" + AutoupdateLabel,
		"launchctl bootstrap gui/501 " + c.PlistPath(),
		"launchctl print gui/501/" + AutoupdateLabel,
	}, r.joined())

	data, err := os.ReadFile(c.PlistPath())
	require.NoError(t, err)
	assert.Equal(t, string(c.LaunchdPlist()), string(data))
	info, err := os.Stat(c.PlistPath())
	require.NoError(t, err)
	assert.Equal(t, os.FileMode(0o644), info.Mode().Perm())
	leftovers, _ := filepath.Glob(filepath.Join(c.LaunchAgentsDir, ".*.tmp-*"))
	assert.Empty(t, leftovers, "atomic write leaves no temp file")

	st := QueryTimerStatus(c, r)
	assert.True(t, st.Installed)
	assert.Equal(t, "launchd", st.Kind)
	assert.Equal(t, c.PlistPath(), st.Path)
	assert.True(t, st.Active)
	assert.Equal(t, "daily at 07:23 local time", st.Detail)

	// Re-install replaces the file; the sequence is identical (idempotent).
	c.Minute = 41
	plan, err = InstallTimerPlan(c)
	require.NoError(t, err)
	r2 := newFakeRunner()
	require.NoError(t, plan.Execute(r2, discardLogger()))
	data, _ = os.ReadFile(c.PlistPath())
	assert.Contains(t, string(data), "<integer>41</integer>")
	assert.Len(t, r2.calls, 3)
}

func TestInstallTimerPlan_DarwinBootstrapFailureStops(t *testing.T) {
	c := darwinTimerConfig(t)
	plan, err := InstallTimerPlan(c)
	require.NoError(t, err)
	r := newFakeRunner()
	r.on("launchctl bootstrap gui/501 "+c.PlistPath(), fakeReply{out: "Bootstrap failed: 5: Input/output error", err: exitErr(5)})
	err = plan.Execute(r, discardLogger())
	require.Error(t, err)
	assert.Contains(t, err.Error(), "load timer")
	assert.Contains(t, err.Error(), "Input/output error")
	assert.Len(t, r.calls, 2, "verify never runs after a failed bootstrap")
}

func TestUninstallTimerPlan_Darwin(t *testing.T) {
	c := darwinTimerConfig(t)
	plan, err := UninstallTimerPlan(c)
	require.NoError(t, err)
	assert.Empty(t, plan.Steps, "nothing installed: empty plan")
	assert.Equal(t, "none", QueryTimerStatus(c, nil).Kind)

	require.NoError(t, os.MkdirAll(c.LaunchAgentsDir, 0o755))
	require.NoError(t, os.WriteFile(c.PlistPath(), c.LaunchdPlist(), 0o644))
	plan, err = UninstallTimerPlan(c)
	require.NoError(t, err)
	r := newFakeRunner()
	require.NoError(t, plan.Execute(r, discardLogger()))
	assert.Equal(t, []string{"launchctl bootout gui/501/" + AutoupdateLabel}, r.joined())
	assert.NoFileExists(t, c.PlistPath())
}

func TestInstallAndUninstallTimerPlan_Linux(t *testing.T) {
	c := linuxTimerConfig(t)
	plan, err := InstallTimerPlan(c)
	require.NoError(t, err)
	r := newFakeRunner()
	r.on("systemctl --user is-active "+SystemdTimerTimer, fakeReply{out: "active\n"})
	require.NoError(t, plan.Execute(r, discardLogger()))
	assert.Equal(t, []string{
		"systemctl --user daemon-reload",
		"systemctl --user enable --now " + SystemdTimerTimer,
		"systemctl --user is-active " + SystemdTimerTimer,
	}, r.joined())
	assert.FileExists(t, c.ServicePath())
	assert.FileExists(t, c.TimerPath())

	st := QueryTimerStatus(c, r)
	assert.True(t, st.Installed)
	assert.Equal(t, "systemd", st.Kind)
	assert.True(t, st.Active)

	plan, err = UninstallTimerPlan(c)
	require.NoError(t, err)
	r2 := newFakeRunner()
	r2.on("systemctl --user disable --now "+SystemdTimerTimer, fakeReply{out: "Failed to disable unit: Unit file does not exist.", err: exitErr(1)})
	require.NoError(t, plan.Execute(r2, discardLogger()), "disable failure is tolerated so the files still go away")
	assert.Equal(t, []string{
		"systemctl --user disable --now " + SystemdTimerTimer,
		"systemctl --user daemon-reload",
	}, r2.joined())
	assert.NoFileExists(t, c.ServicePath())
	assert.NoFileExists(t, c.TimerPath())
}

func TestTimerPlan_UnsupportedOS(t *testing.T) {
	c := darwinTimerConfig(t)
	c.GOOS = "windows"
	_, err := InstallTimerPlan(c)
	assert.ErrorContains(t, err, "not supported on windows")
	_, err = UninstallTimerPlan(c)
	assert.ErrorContains(t, err, "not supported on windows")
	assert.Equal(t, "none", QueryTimerStatus(c, nil).Kind)
}

func TestPlanDescribe_DryRunListsFilesAndCommands(t *testing.T) {
	c := darwinTimerConfig(t)
	plan, err := InstallTimerPlan(c)
	require.NoError(t, err)
	desc := plan.Describe()
	assert.Contains(t, desc, "[1] write "+c.PlistPath()+" (mode 0644):")
	assert.Contains(t, desc, "    <key>Label</key>")
	assert.Contains(t, desc, "[2] run   launchctl bootout gui/501/"+AutoupdateLabel)
	assert.Contains(t, desc, "[3] run   launchctl bootstrap gui/501 "+c.PlistPath())
	assert.Contains(t, desc, "[4] run   launchctl print gui/501/"+AutoupdateLabel)
	assert.NoFileExists(t, c.PlistPath(), "Describe writes nothing")
}

func TestShellQuote(t *testing.T) {
	assert.Equal(t, "launchctl bootstrap gui/501 '/Users/me/my apps/x.plist'", ShellQuote([]string{"launchctl", "bootstrap", "gui/501", "/Users/me/my apps/x.plist"}))
	assert.Equal(t, `'it'\''s'`, ShellQuote([]string{"it's"}))
}

func TestAcquireUpdateLock(t *testing.T) {
	dir := t.TempDir()
	release, busy, err := AcquireUpdateLock(dir, UpdateLockStaleAfter)
	require.NoError(t, err)
	require.False(t, busy)
	require.NotNil(t, release)
	lockPath := filepath.Join(dir, UpdateLockFileName)
	assert.FileExists(t, lockPath)

	_, busy, err = AcquireUpdateLock(dir, UpdateLockStaleAfter)
	require.NoError(t, err)
	assert.True(t, busy, "second acquirer sees the live lock")

	release()
	assert.NoFileExists(t, lockPath)

	// A stale lock (older than the threshold) is taken over.
	require.NoError(t, os.WriteFile(lockPath, []byte("1 old\n"), 0o644))
	old := time.Now().Add(-UpdateLockStaleAfter - time.Minute)
	require.NoError(t, os.Chtimes(lockPath, old, old))
	release2, busy, err := AcquireUpdateLock(dir, UpdateLockStaleAfter)
	require.NoError(t, err)
	assert.False(t, busy)
	content, _ := os.ReadFile(lockPath)
	assert.False(t, strings.HasPrefix(string(content), "1 old"), "stale lock was replaced")
	release2()
}
