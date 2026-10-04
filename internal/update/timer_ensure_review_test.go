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

// Regression tests for the #2472 verifier round 1 findings 1, 3 and 4.

func backupsIn(t *testing.T, dir string) []string {
	t.Helper()
	m, err := filepath.Glob(filepath.Join(dir, "*"+legacyBackupSuffix+"*"))
	require.NoError(t, err)
	return m
}

// Finding 1: the automatic heal must leave an active canonical timer its
// owner customised as it is: no rewrite, no reload, no enable.
func TestEnsureTimer_AutoLeavesACustomisedActiveCanonicalAlone(t *testing.T) {
	c := linuxTimerConfig(t)
	r := fakeSystemctl(t, c, shapeCanonicalActive)
	custom := strings.Replace(string(c.SystemdTimer()), "OnCalendar=daily", "OnCalendar=hourly", 1)
	require.NoError(t, os.WriteFile(c.TimerPath(), []byte(custom), 0o644))
	// A pin to another installed binary is an owner's choice too.
	other := filepath.Join(t.TempDir(), "agent-deck")
	require.NoError(t, os.WriteFile(other, []byte("x"), 0o755))
	customSvc := strings.Replace(string(c.SystemdService()), c.Exe, other, 1)
	require.NoError(t, os.WriteFile(c.ServicePath(), []byte(customSvc), 0o644))

	res, err := EnsureTimer(c, r, true, discardLogger())
	require.NoError(t, err)
	assert.Equal(t, TimerActionNone, res.Action)
	assert.False(t, res.Changed())
	assert.Empty(t, mutating(r), "an active canonical timer is never touched by the automatic heal")
	assert.Equal(t, custom, readFile(t, c.TimerPath()))
	assert.Equal(t, customSvc, readFile(t, c.ServicePath()))
	assert.Empty(t, backupsIn(t, c.SystemdUserDir))
}

// Finding 1, the repair the automatic heal still makes: a service pinned
// to a binary that no longer exists is rewritten, and the owner's version
// of every replaced file is kept as a backup.
func TestEnsureTimer_AutoRepairsAPinToAMissingBinaryWithBackup(t *testing.T) {
	c := linuxTimerConfig(t)
	c.Now = func() time.Time { return time.Date(2026, 10, 3, 9, 0, 0, 0, time.UTC) }
	r := fakeSystemctl(t, c, shapeCanonicalActive)
	gone := filepath.Join(t.TempDir(), "removed", "agent-deck")
	staleSvc := strings.Replace(string(c.SystemdService()), c.Exe, gone, 1)
	require.NoError(t, os.WriteFile(c.ServicePath(), []byte(staleSvc), 0o644))
	custom := strings.Replace(string(c.SystemdTimer()), "OnCalendar=daily", "OnCalendar=hourly", 1)
	require.NoError(t, os.WriteFile(c.TimerPath(), []byte(custom), 0o644))

	res, err := EnsureTimer(c, r, true, discardLogger())
	require.NoError(t, err)
	assert.Equal(t, TimerActionInstalled, res.Action)
	assert.Equal(t, string(c.SystemdService()), readFile(t, c.ServicePath()))
	require.Equal(t, []string{
		c.ServicePath() + ".bak-agentdeck-20261003-090000",
		c.TimerPath() + ".bak-agentdeck-20261003-090000",
	}, res.Backups)
	assert.Equal(t, staleSvc, readFile(t, res.Backups[0]))
	assert.Equal(t, custom, readFile(t, res.Backups[1]))
}

// Finding 1, the explicit form: --install-timer may bring a customised
// pair back to what this binary renders, but never without a backup.
func TestEnsureTimer_ExplicitRewriteBacksUpTheOwnersFile(t *testing.T) {
	c := linuxTimerConfig(t)
	r := fakeSystemctl(t, c, shapeCanonicalActive)
	custom := strings.Replace(string(c.SystemdTimer()), "OnCalendar=daily", "OnCalendar=hourly", 1)
	require.NoError(t, os.WriteFile(c.TimerPath(), []byte(custom), 0o644))

	res, err := EnsureTimer(c, r, false, discardLogger())
	require.NoError(t, err)
	assert.Equal(t, TimerActionInstalled, res.Action)
	assert.Equal(t, string(c.SystemdTimer()), readFile(t, c.TimerPath()))
	require.Len(t, res.Backups, 1)
	assert.Equal(t, custom, readFile(t, res.Backups[0]))
}

func TestExecStartBinary(t *testing.T) {
	assert.Equal(t, "/usr/bin/agent-deck", execStartBinary("[Service]\nExecStart=/usr/bin/agent-deck update\n"))
	assert.Equal(t, "/opt/my apps/agent-deck", execStartBinary("ExecStart=-\"/opt/my apps/agent-deck\" update\n"))
	assert.Equal(t, `/a"b/agent-deck`, execStartBinary(`ExecStart="/a\"b/agent-deck" update`))
	assert.Equal(t, "/bin/bash", execStartBinary(legacyServiceUnit))
	assert.Equal(t, "", execStartBinary("[Service]\nType=oneshot\n"))
}

// readOnlyLegacyDir lays out a legacy pair in a directory this process
// cannot write, the shape of a unit an admin put in /etc/systemd/user.
func readOnlyLegacyDir(t *testing.T, c TimerConfig) (dir, legacyTimer string) {
	t.Helper()
	dir = filepath.Join(c.Home, "etc-systemd-user")
	require.NoError(t, os.MkdirAll(dir, 0o755))
	legacyTimer = filepath.Join(dir, LegacySystemdTimerTimer)
	require.NoError(t, os.WriteFile(legacyTimer, []byte(legacyTimerUnit), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(dir, LegacySystemdTimerService), []byte(legacyServiceUnit), 0o644))
	require.NoError(t, os.Chmod(dir, 0o555))
	t.Cleanup(func() { _ = os.Chmod(dir, 0o755) })
	if f, err := os.CreateTemp(dir, "probe"); err == nil {
		// Only a process with CAP_DAC_OVERRIDE gets here.
		_ = f.Close()
		_ = os.Remove(f.Name())
		t.Skip("this process can write a 0555 directory")
	}
	require.False(t, dirWritable(dir))
	return dir, legacyTimer
}

// Finding 3: a legacy unit in a directory this user cannot write is never
// moved, so the automatic heal neither fails nor repeats its disable on
// every run; an active one is stopped once, then left alone with a note.
func TestEnsureTimer_LegacyInReadOnlyDirIsLeftInPlaceWithoutFailing(t *testing.T) {
	c := linuxTimerConfig(t)
	_ = fakeSystemctl(t, c, shapeCanonicalActive) // lays out the active canonical pair
	dir, legacy := readOnlyLegacyDir(t, c)

	run := func(legacyActive bool) (TimerEnsureResult, *fakeRunner, error) {
		r := newFakeRunner()
		r.on("systemctl --user cat "+LegacySystemdTimerTimer, fakeReply{out: "# " + legacy + "\n" + legacyTimerUnit})
		if legacyActive {
			r.on("systemctl --user is-active "+LegacySystemdTimerTimer, fakeReply{out: "active\n"})
		} else {
			r.on("systemctl --user is-active "+LegacySystemdTimerTimer, fakeReply{out: "inactive\n", err: exitErr(3)})
		}
		r.on("systemctl --user is-active "+SystemdTimerTimer, fakeReply{out: "active\n"})
		r.on("systemctl --user is-system-running", fakeReply{out: "running\n"})
		res, err := EnsureTimer(c, r, true, discardLogger())
		return res, r, err
	}

	// Active: stopped, never moved, no error.
	res, r, err := run(true)
	require.NoError(t, err)
	assert.Equal(t, []string{"systemctl --user disable --now " + LegacySystemdTimerTimer}, mutating(r))
	assert.Contains(t, res.Note, "not writable")
	assert.Contains(t, res.Note, "stopped it")
	assert.Contains(t, res.Line(), dir)
	// Only stopped, not moved: the result must not claim a migration
	// (#2472 review round 2, finding 2).
	assert.Equal(t, TimerActionStopped, res.Action)
	assert.Empty(t, res.Migrated)
	assert.True(t, res.Changed())
	assert.True(t, strings.HasPrefix(res.Line(), "stopped legacy timer "+LegacySystemdTimerTimer), res.Line())
	assert.NotContains(t, res.Line(), "migrated")

	// Stopped: later runs change nothing and do not fail.
	for i := 0; i < 2; i++ {
		res, r, err = run(false)
		require.NoError(t, err, "run %d", i)
		assert.Equal(t, TimerActionNone, res.Action, "run %d", i)
		assert.Empty(t, mutating(r), "run %d", i)
		assert.Contains(t, res.Note, "left in place")
	}
	assert.Equal(t, legacyTimerUnit, readFile(t, legacy))
	assert.Equal(t, legacyServiceUnit, readFile(t, filepath.Join(dir, LegacySystemdTimerService)))
}

// Finding 4: a lone hand-made service (its timer already gone) is named by
// the status and backed up by the heal, never deleted.
func TestEnsureTimer_LoneLegacyServiceIsReportedAndBackedUp(t *testing.T) {
	c := linuxTimerConfig(t)
	c.Now = func() time.Time { return time.Date(2026, 10, 3, 9, 0, 0, 0, time.UTC) }
	r := fakeSystemctl(t, c, shapeNoTimer)
	require.NoError(t, os.MkdirAll(c.SystemdUserDir, 0o755))
	svc := filepath.Join(c.SystemdUserDir, LegacySystemdTimerService)
	require.NoError(t, os.WriteFile(svc, []byte(legacyServiceUnit), 0o644))
	r.on("systemctl --user is-active "+SystemdTimerTimer, fakeReply{out: "active\n"})

	st := QueryTimerStatus(c, r)
	assert.Equal(t, TimerKindNone, st.Kind, "a service alone runs nothing")
	assert.Equal(t, LegacySystemdTimerService, st.LegacyUnit)
	assert.Equal(t, svc, st.LegacyPath)

	res, err := EnsureTimer(c, r, true, discardLogger())
	require.NoError(t, err)
	assert.Equal(t, TimerActionMigrated, res.Action)
	assert.Equal(t, LegacySystemdTimerService, res.Migrated)
	assert.Equal(t, "migrated legacy service agentdeck-autoupdate.service -> agent-deck-autoupdate.timer", res.Line())
	assert.NotContains(t, strings.Join(mutating(r), "\n"), "disable", "there is no legacy timer to disable")
	assert.NoFileExists(t, svc)
	require.Equal(t, []string{svc + ".bak-agentdeck-20261003-090000"}, res.Backups)
	assert.Equal(t, legacyServiceUnit, readFile(t, res.Backups[0]))
	assert.Equal(t, string(c.SystemdTimer()), readFile(t, c.TimerPath()))
	assert.Empty(t, res.Status.LegacyUnit)
}
