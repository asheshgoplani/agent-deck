package update

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// #2472: the fake systemctl answers the queries the timer code makes (and
// list-unit-files / is-enabled, which an operator would use to check by
// hand) for the five host shapes seen in the field. It records every call;
// nothing in these tests reaches a real systemctl.

const legacyTimerUnit = `[Unit]
Description=agent-deck daily update

[Timer]
OnCalendar=daily
Persistent=true

[Install]
WantedBy=timers.target
`

const legacyServiceUnit = `[Service]
Type=oneshot
ExecStart=/bin/bash -lc 'P=$(command -v agent-deck || echo "$HOME/.local/bin/agent-deck"); "$P" update'
`

type systemctlShape int

const (
	shapeNoTimer systemctlShape = iota
	shapeCanonicalActive
	shapeLegacyOnly
	shapeBothPresent
	shapeNoUserBus
)

// fakeSystemctl lays out the unit files for shape under c.SystemdUserDir
// and returns a fake runner answering for it.
func fakeSystemctl(t *testing.T, c TimerConfig, shape systemctlShape) *fakeRunner {
	t.Helper()
	r := newFakeRunner()
	write := func(name, content string) {
		require.NoError(t, os.MkdirAll(c.SystemdUserDir, 0o755))
		require.NoError(t, os.WriteFile(filepath.Join(c.SystemdUserDir, name), []byte(content), 0o644))
	}
	showUTC := "systemctl --user show --timestamp=utc -p LastTriggerUSec -p NextElapseUSecRealtime "
	switch shape {
	case shapeNoTimer:
		r.on("systemctl --user list-unit-files", fakeReply{out: "0 unit files listed.\n"})
		r.on("systemctl --user cat "+LegacySystemdTimerTimer, fakeReply{out: "No files found for agentdeck-autoupdate.timer.\n", err: exitErr(1)})
		r.on("systemctl --user is-system-running", fakeReply{out: "running\n"})
	case shapeCanonicalActive, shapeBothPresent:
		write(SystemdTimerService, string(c.SystemdService()))
		write(SystemdTimerTimer, string(c.SystemdTimer()))
		r.on("systemctl --user is-active "+SystemdTimerTimer, fakeReply{out: "active\n"})
		r.on("systemctl --user is-enabled "+SystemdTimerTimer, fakeReply{out: "enabled\n"})
		r.on(showUTC+SystemdTimerTimer, fakeReply{out: "LastTriggerUSec=Fri 2026-10-02 22:59:12 UTC\nNextElapseUSecRealtime=Sat 2026-10-03 23:12:00 UTC\n"})
		r.on("systemctl --user is-system-running", fakeReply{out: "degraded\n", err: exitErr(1)})
		if shape == shapeCanonicalActive {
			r.on("systemctl --user cat "+LegacySystemdTimerTimer, fakeReply{out: "No files found for agentdeck-autoupdate.timer.\n", err: exitErr(1)})
			break
		}
		fallthrough
	case shapeLegacyOnly:
		write(LegacySystemdTimerTimer, legacyTimerUnit)
		write(LegacySystemdTimerService, legacyServiceUnit)
		r.on("systemctl --user list-unit-files", fakeReply{out: "agentdeck-autoupdate.service static\nagentdeck-autoupdate.timer enabled\n"})
		r.on("systemctl --user is-active "+LegacySystemdTimerTimer, fakeReply{out: "active\n"})
		r.on("systemctl --user is-enabled "+LegacySystemdTimerTimer, fakeReply{out: "enabled\n"})
		r.on(showUTC+LegacySystemdTimerTimer, fakeReply{out: "LastTriggerUSec=Fri 2026-10-03 00:01:44 UTC\nNextElapseUSecRealtime=Sat 2026-10-04 00:00:00 UTC\n"})
		r.on("systemctl --user is-system-running", fakeReply{out: "running\n"})
	case shapeNoUserBus:
		write(SystemdTimerService, string(c.SystemdService()))
		write(SystemdTimerTimer, string(c.SystemdTimer()))
		bus := fakeReply{out: "Failed to connect to bus: No medium found\n", err: exitErr(1)}
		r.on("systemctl --user is-active "+SystemdTimerTimer, bus)
		r.on("systemctl --user is-system-running", bus)
		r.on("systemctl --user cat "+LegacySystemdTimerTimer, bus)
		r.on(showUTC+SystemdTimerTimer, bus)
		r.on("systemctl --user show -p LastTriggerUSec -p NextElapseUSecRealtime "+SystemdTimerTimer, bus)
	}
	return r
}

// mutating lists the calls that change the host (everything but queries).
func mutating(r *fakeRunner) []string {
	var out []string
	for _, c := range r.joined() {
		switch {
		case strings.Contains(c, " is-active "), strings.Contains(c, " is-enabled "), strings.Contains(c, " cat "),
			strings.Contains(c, " show "), strings.HasSuffix(c, " is-system-running"), strings.HasPrefix(c, "launchctl print"):
			continue
		}
		out = append(out, c)
	}
	return out
}

func TestQueryTimerStatus_LegacyOnlyIsInstalledNotNone(t *testing.T) {
	c := linuxTimerConfig(t)
	r := fakeSystemctl(t, c, shapeLegacyOnly)
	st := QueryTimerStatus(c, r)
	legacy := filepath.Join(c.SystemdUserDir, LegacySystemdTimerTimer)
	assert.Equal(t, TimerKindSystemdLegacy, st.Kind, "a working hand-made timer is never 'none'")
	assert.True(t, st.Installed)
	assert.True(t, st.Active)
	assert.Equal(t, legacy, st.Path)
	assert.Equal(t, LegacySystemdTimerTimer, st.LegacyUnit)
	assert.Equal(t, legacy, st.LegacyPath)
	assert.Equal(t, "daily (hand-made legacy unit)", st.Detail)
	assert.Equal(t, "2026-10-03T00:01:44Z", st.LastRun)
	assert.Equal(t, "2026-10-04T00:00:00Z", st.NextRun)
	assert.Empty(t, mutating(r), "status never changes the host")

	// Without a runner the file alone is enough to say so.
	st = QueryTimerStatus(c, nil)
	assert.Equal(t, TimerKindSystemdLegacy, st.Kind)
	assert.False(t, st.Active)
}

func TestQueryTimerStatus_LegacyFoundThroughSystemctlCat(t *testing.T) {
	c := linuxTimerConfig(t)
	elsewhere := filepath.Join(c.Home, ".local", "share", "systemd", "user", LegacySystemdTimerTimer)
	r := newFakeRunner()
	r.on("systemctl --user cat "+LegacySystemdTimerTimer, fakeReply{out: "# " + elsewhere + "\n" + legacyTimerUnit})
	r.on("systemctl --user is-active "+LegacySystemdTimerTimer, fakeReply{out: "inactive\n", err: exitErr(3)})
	st := QueryTimerStatus(c, r)
	assert.Equal(t, TimerKindSystemdLegacy, st.Kind)
	assert.Equal(t, elsewhere, st.Path)
	assert.False(t, st.Active)
	assert.Equal(t, "daily (hand-made legacy unit)", st.Detail)
}

func TestQueryTimerStatus_Shapes(t *testing.T) {
	c := linuxTimerConfig(t)
	st := QueryTimerStatus(c, fakeSystemctl(t, c, shapeNoTimer))
	assert.Equal(t, TimerStatus{Kind: TimerKindNone, Path: c.TimerPath()}, st)

	c = linuxTimerConfig(t)
	st = QueryTimerStatus(c, fakeSystemctl(t, c, shapeCanonicalActive))
	assert.Equal(t, TimerKindSystemd, st.Kind)
	assert.True(t, st.Active)
	assert.Empty(t, st.LegacyUnit)
	assert.Equal(t, "2026-10-02T22:59:12Z", st.LastRun)
	assert.Equal(t, "2026-10-03T23:12:00Z", st.NextRun)

	c = linuxTimerConfig(t)
	st = QueryTimerStatus(c, fakeSystemctl(t, c, shapeBothPresent))
	assert.Equal(t, TimerKindSystemd, st.Kind, "the canonical timer is the one reported")
	assert.Equal(t, LegacySystemdTimerTimer, st.LegacyUnit, "the legacy one is named so it gets retired")
	assert.Equal(t, filepath.Join(c.SystemdUserDir, LegacySystemdTimerTimer), st.LegacyPath)

	c = linuxTimerConfig(t)
	st = QueryTimerStatus(c, fakeSystemctl(t, c, shapeNoUserBus))
	assert.Equal(t, TimerKindSystemd, st.Kind)
	assert.False(t, st.Active)
	assert.Contains(t, st.Note, "no systemd user session")
	assert.Contains(t, st.Note, "Failed to connect to bus")
}

func TestEnsureTimer_MigratesLegacyPairOnce(t *testing.T) {
	c := linuxTimerConfig(t)
	c.Now = func() time.Time { return time.Date(2026, 10, 3, 9, 0, 0, 0, time.UTC) }
	r := fakeSystemctl(t, c, shapeLegacyOnly)
	// The canonical timer answers active once enabled.
	r.on("systemctl --user is-active "+SystemdTimerTimer, fakeReply{out: "active\n"})

	res, err := EnsureTimer(c, r, false, discardLogger())
	require.NoError(t, err)
	assert.Equal(t, TimerActionMigrated, res.Action)
	assert.Equal(t, "migrated legacy timer agentdeck-autoupdate.timer -> agent-deck-autoupdate.timer", res.Line())

	// Canonical first and verified, legacy retired after: a failure in the
	// retirement never leaves the host without a timer.
	assert.Equal(t, []string{
		"systemctl --user daemon-reload",
		"systemctl --user enable --now " + SystemdTimerTimer,
		"systemctl --user disable --now " + LegacySystemdTimerTimer,
		"systemctl --user daemon-reload",
	}, mutating(r))
	assert.Equal(t, string(c.SystemdService()), readFile(t, c.ServicePath()))
	assert.Equal(t, string(c.SystemdTimer()), readFile(t, c.TimerPath()))

	// The hand-made units are moved aside, never deleted.
	legacyTimer := filepath.Join(c.SystemdUserDir, LegacySystemdTimerTimer)
	legacySvc := filepath.Join(c.SystemdUserDir, LegacySystemdTimerService)
	assert.NoFileExists(t, legacyTimer)
	assert.NoFileExists(t, legacySvc)
	require.Equal(t, []string{legacyTimer + ".bak-agentdeck-20261003-090000", legacySvc + ".bak-agentdeck-20261003-090000"}, res.Backups)
	assert.Equal(t, legacyTimerUnit, readFile(t, res.Backups[0]))
	assert.Equal(t, legacyServiceUnit, readFile(t, res.Backups[1]))

	assert.Equal(t, TimerKindSystemd, res.Status.Kind)
	assert.True(t, res.Status.Active)
	assert.Empty(t, res.Status.LegacyUnit)

	// Idempotent: a second run finds the active canonical timer and does
	// nothing (systemctl cat no longer finds the legacy unit).
	r2 := newFakeRunner()
	r2.on("systemctl --user is-active "+SystemdTimerTimer, fakeReply{out: "active\n"})
	r2.on("systemctl --user cat "+LegacySystemdTimerTimer, fakeReply{out: "No files found\n", err: exitErr(1)})
	res, err = EnsureTimer(c, r2, false, discardLogger())
	require.NoError(t, err)
	assert.Equal(t, TimerActionNone, res.Action)
	assert.Empty(t, mutating(r2))
}

func TestEnsureTimer_BothPresentRetiresOnlyTheLegacyUnit(t *testing.T) {
	c := linuxTimerConfig(t)
	r := fakeSystemctl(t, c, shapeBothPresent)
	res, err := EnsureTimer(c, r, true, discardLogger())
	require.NoError(t, err)
	assert.Equal(t, TimerActionMigrated, res.Action)
	assert.Equal(t, []string{
		"systemctl --user disable --now " + LegacySystemdTimerTimer,
		"systemctl --user daemon-reload",
	}, mutating(r), "the active, current canonical timer is left alone")
	assert.Len(t, res.Backups, 2)
}

func TestEnsureTimer_ActiveCanonicalIsLeftAlone(t *testing.T) {
	for _, auto := range []bool{false, true} {
		c := linuxTimerConfig(t)
		r := fakeSystemctl(t, c, shapeCanonicalActive)
		res, err := EnsureTimer(c, r, auto, discardLogger())
		require.NoError(t, err)
		assert.Equal(t, TimerActionNone, res.Action, "auto=%v", auto)
		assert.Empty(t, mutating(r), "auto=%v", auto)
		assert.False(t, res.Changed())
	}
}

func TestEnsureTimer_InstallsWhereNoneIsActive(t *testing.T) {
	c := linuxTimerConfig(t)
	r := fakeSystemctl(t, c, shapeNoTimer)
	r.on("systemctl --user is-active "+SystemdTimerTimer, fakeReply{out: "active\n"})
	res, err := EnsureTimer(c, r, true, discardLogger())
	require.NoError(t, err)
	assert.Equal(t, TimerActionInstalled, res.Action)
	assert.True(t, res.Changed())
	assert.Equal(t, []string{"systemctl --user daemon-reload", "systemctl --user enable --now " + SystemdTimerTimer}, mutating(r))
	assert.True(t, res.Status.Installed)

	// An installed but inactive canonical timer is re-enabled, not
	// rewritten.
	r2 := newFakeRunner()
	r2.on("systemctl --user is-active "+SystemdTimerTimer, fakeReply{out: "inactive\n", err: exitErr(3)}, fakeReply{out: "active\n"})
	res, err = EnsureTimer(c, r2, true, discardLogger())
	require.NoError(t, err)
	assert.Equal(t, TimerActionLoaded, res.Action)
	assert.Equal(t, []string{"systemctl --user daemon-reload", "systemctl --user enable --now " + SystemdTimerTimer}, mutating(r2))
}

func TestEnsureTimer_StaleCanonicalUnitIsRewritten(t *testing.T) {
	c := linuxTimerConfig(t)
	r := fakeSystemctl(t, c, shapeCanonicalActive)
	require.NoError(t, os.WriteFile(c.ServicePath(), []byte("[Service]\nExecStart=/old/agent-deck update\n"), 0o644))
	res, err := EnsureTimer(c, r, false, discardLogger())
	require.NoError(t, err)
	assert.Equal(t, TimerActionInstalled, res.Action)
	assert.Equal(t, string(c.SystemdService()), readFile(t, c.ServicePath()))
	// The replaced file is kept, the unchanged timer is not backed up.
	require.Len(t, res.Backups, 1)
	assert.Equal(t, "[Service]\nExecStart=/old/agent-deck update\n", readFile(t, res.Backups[0]))
}

func TestEnsureTimer_NoUserBusIsASkipNotAWrite(t *testing.T) {
	c := linuxTimerConfig(t)
	r := fakeSystemctl(t, c, shapeNoUserBus)
	res, err := EnsureTimer(c, r, true, discardLogger())
	require.NoError(t, err)
	assert.Equal(t, TimerActionSkipped, res.Action)
	assert.Contains(t, res.Reason, "no systemd user session")
	assert.Empty(t, mutating(r))

	// systemctl missing entirely (exec error without an exit status).
	c = linuxTimerConfig(t)
	r = newFakeRunner()
	r.on("systemctl --user is-system-running", fakeReply{err: errors.New(`exec: "systemctl": executable file not found in $PATH`)})
	res, err = EnsureTimer(c, r, true, discardLogger())
	require.NoError(t, err)
	assert.Equal(t, TimerActionSkipped, res.Action)
	assert.NoFileExists(t, c.TimerPath())
}

func TestEnsureTimer_AutoNeverPinsAnUnpinnableBuild(t *testing.T) {
	c := linuxTimerConfig(t)
	c.Unpinnable = "unpinnable dev build: /tmp/go-build1/agent-deck is not in an install directory"
	r := fakeSystemctl(t, c, shapeNoTimer)
	res, err := EnsureTimer(c, r, true, discardLogger())
	require.NoError(t, err)
	assert.Equal(t, TimerActionSkipped, res.Action)
	assert.Contains(t, res.Reason, "unpinnable")
	assert.Empty(t, mutating(r))
	assert.NoFileExists(t, c.TimerPath())
}

func TestEnsureTimer_BackupNeverOverwritesAnEarlierOne(t *testing.T) {
	c := linuxTimerConfig(t)
	c.Now = func() time.Time { return time.Date(2026, 10, 3, 9, 0, 0, 0, time.UTC) }
	r := fakeSystemctl(t, c, shapeLegacyOnly)
	earlier := filepath.Join(c.SystemdUserDir, LegacySystemdTimerTimer+".bak-agentdeck-20261003-090000")
	require.NoError(t, os.WriteFile(earlier, []byte("earlier backup"), 0o644))
	res, err := EnsureTimer(c, r, false, discardLogger())
	require.NoError(t, err)
	assert.Equal(t, earlier+".1", res.Backups[0])
	assert.Equal(t, "earlier backup", readFile(t, earlier))
}

func TestPlanEnsureTimer_DescribeShowsTheMigration(t *testing.T) {
	c := linuxTimerConfig(t)
	r := fakeSystemctl(t, c, shapeLegacyOnly)
	p, err := PlanEnsureTimer(c, r, false)
	require.NoError(t, err)
	desc := p.Describe()
	assert.Contains(t, desc, "run   systemctl --user enable --now "+SystemdTimerTimer)
	assert.Contains(t, desc, "run   systemctl --user disable --now "+LegacySystemdTimerTimer)
	assert.Contains(t, desc, "move  "+filepath.Join(c.SystemdUserDir, LegacySystemdTimerTimer)+" -> ")
	assert.Empty(t, mutating(r), "planning changes nothing")
	assert.FileExists(t, filepath.Join(c.SystemdUserDir, LegacySystemdTimerTimer))
}

func TestEnsureTimer_DarwinAutomaticNeverReplacesALoadedPlist(t *testing.T) {
	c := darwinTimerConfig(t)
	target := "gui/501/" + AutoupdateLabel

	// Missing: installed with the full plan.
	r := newFakeRunner()
	r.on("launchctl bootout "+target, fakeReply{out: "Boot-out failed: 3: No such process", err: exitErr(3)})
	res, err := EnsureTimer(c, r, true, discardLogger())
	require.NoError(t, err)
	assert.Equal(t, TimerActionInstalled, res.Action)
	assert.FileExists(t, c.PlistPath())

	// Present but not loaded: bootstrapped again, never booted out (the
	// run may be the timer's own job).
	r = newFakeRunner()
	r.on("launchctl print "+target, fakeReply{out: "Could not find service", err: exitErr(113)}, fakeReply{})
	res, err = EnsureTimer(c, r, true, discardLogger())
	require.NoError(t, err)
	assert.Equal(t, TimerActionLoaded, res.Action)
	assert.Equal(t, []string{"launchctl bootstrap gui/501 " + c.PlistPath()}, mutating(r))

	// Loaded with different content: left alone.
	require.NoError(t, os.WriteFile(c.PlistPath(), []byte(strings.Replace(string(c.LaunchdPlist()), "update --unattended", "update --unattended --x", 1)), 0o644))
	r = newFakeRunner()
	res, err = EnsureTimer(c, r, true, discardLogger())
	require.NoError(t, err)
	assert.Equal(t, TimerActionNone, res.Action)
	assert.Empty(t, mutating(r))

	// No gui domain (an ssh session on a Mac): skipped.
	r = newFakeRunner()
	r.on("launchctl print gui/501", fakeReply{out: "Bad request.\nCould not find domain for user gui: 501", err: exitErr(113)})
	res, err = EnsureTimer(c, r, true, discardLogger())
	require.NoError(t, err)
	assert.Equal(t, TimerActionSkipped, res.Action)
	assert.Contains(t, res.Reason, "launchd gui domain unavailable")
}

func TestEnsureTimer_DarwinExplicitKeepsTheScheduleAndIsIdempotent(t *testing.T) {
	c := darwinTimerConfig(t)
	c.Minute = 23
	r := newFakeRunner()
	_, err := EnsureTimer(c, r, false, discardLogger())
	require.NoError(t, err)

	c.Minute = 41 // a fresh random draw must not count as "stale"
	r = newFakeRunner()
	res, err := EnsureTimer(c, r, false, discardLogger())
	require.NoError(t, err)
	assert.Equal(t, TimerActionNone, res.Action)
	assert.Empty(t, mutating(r))
	assert.Contains(t, readFile(t, c.PlistPath()), "<integer>23</integer>")
}

func TestStableExecutablePath(t *testing.T) {
	dir := t.TempDir()
	bin := filepath.Join(dir, "bin")
	cellar := filepath.Join(dir, "Cellar", "agent-deck", "1.16.24", "bin")
	require.NoError(t, os.MkdirAll(bin, 0o755))
	require.NoError(t, os.MkdirAll(cellar, 0o755))
	real := filepath.Join(cellar, "agent-deck")
	require.NoError(t, os.WriteFile(real, []byte("x"), 0o755))
	link := filepath.Join(bin, "agent-deck")
	require.NoError(t, os.Symlink(real, link))

	assert.Equal(t, link, StableExecutablePath(real, []string{bin}), "the bin symlink, never the versioned keg")
	assert.Equal(t, link, StableExecutablePath(link, []string{bin}))
	assert.Empty(t, StableExecutablePath(real, []string{filepath.Join(dir, "elsewhere")}), "a dev build is unpinnable")
}

func TestSystemdTimestamp(t *testing.T) {
	assert.Equal(t, "2026-10-03T22:59:12Z", systemdTimestamp("Sat 2026-10-03 22:59:12 UTC"))
	assert.Equal(t, "", systemdTimestamp("n/a"))
	assert.Equal(t, "", systemdTimestamp(""))
	assert.Equal(t, "in 3h", systemdTimestamp("in 3h"), "unparsable text is kept, not dropped")
	// #2472 review nit: an abbreviation the local zone does not know must
	// not be read as UTC.
	foreign := "Sat 2026-10-03 22:59:12 ZZT"
	assert.Equal(t, foreign, systemdTimestamp(foreign), "an unknown zone is kept raw, never read as offset 0")
}

func readFile(t *testing.T, p string) string {
	t.Helper()
	data, err := os.ReadFile(p)
	require.NoError(t, err)
	return string(data)
}
