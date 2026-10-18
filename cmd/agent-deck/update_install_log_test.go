package main

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/asheshgoplani/agent-deck/internal/telemetry"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// useTempInstallLog points appendInstallLogLine at a file under t.TempDir.
func useTempInstallLog(t *testing.T) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "logs", "auto-update.log")
	prev := installLogPath
	installLogPath = func() (string, error) { return path, nil }
	t.Cleanup(func() { installLogPath = prev })
	return path
}

func readInstallLog(t *testing.T, path string) []string {
	t.Helper()
	b, err := os.ReadFile(path)
	require.NoError(t, err)
	return strings.Split(strings.TrimRight(string(b), "\n"), "\n")
}

// Every install path writes one line naming the path that replaced the
// binary. A Mac CLI swapped at 00:27:35 on 2026-10-19 left no line in
// auto-update.log because only the launchd timer's output went there.
func TestRecordUpdateAttempt_WritesOneInstallLogLine(t *testing.T) {
	path := useTempInstallLog(t)

	require.NoError(t, recordUpdateAttempt("1.16.29", "1.16.30", telemetry.UpdateManual, "manual", func() error { return nil }))
	boom := errors.New("download\nfailed")
	require.ErrorIs(t, recordUpdateAttempt("1.16.29", "1.16.30", telemetry.UpdateManual, "manual-version", func() error { return boom }), boom)

	lines := readInstallLog(t, path)
	require.Len(t, lines, 2)
	assert.Contains(t, lines[0], " install 1.16.29 -> 1.16.30 ok source=manual pid=")
	assert.Contains(t, lines[1], " install 1.16.29 -> 1.16.30 failed: download failed source=manual-version pid=")

	info, err := os.Stat(path)
	require.NoError(t, err)
	assert.Equal(t, os.FileMode(0o600), info.Mode().Perm())
}

// The unattended path (timer, web daemon, TUI auto install, nudge) names
// its trigger.
func TestRunUnattendedUpdate_InstallLogNamesTrigger(t *testing.T) {
	for _, trigger := range []string{"timer", "web", "tui", "nudge"} {
		t.Run(trigger, func(t *testing.T) {
			path := useTempInstallLog(t)
			h := newUnattendedHarness(t, availableInfo())
			h.deps.trigger = trigger
			require.Equal(t, exitUpdateOK, runUnattendedUpdate(h.deps))

			lines := readInstallLog(t, path)
			require.Len(t, lines, 1)
			assert.Contains(t, lines[0], " install 1.16.5 -> 1.17.0 ok source="+trigger+" ")
		})
	}
}

func TestAppendInstallLogLine_Format(t *testing.T) {
	path := useTempInstallLog(t)
	t.Setenv("XPC_SERVICE_NAME", "com.example.agent")
	at := time.Date(2026, 10, 19, 0, 27, 35, 0, time.FixedZone("CEST", 2*3600))
	appendInstallLogLine(at, "1.16.29", "1.16.30", "web", nil)

	lines := readInstallLog(t, path)
	require.Len(t, lines, 1)
	assert.True(t, strings.HasPrefix(lines[0], "2026-10-19T00:27:35+02:00 install 1.16.29 -> 1.16.30 ok source=web pid="), lines[0])
	assert.True(t, strings.HasSuffix(lines[0], " service=com.example.agent"), lines[0])
}

// A failed append is reported, not dropped: writeInstallLogLine returns the
// write or close error (the caller logs it and never fails the install).
func TestWriteInstallLogLineReportsWriteFailure(t *testing.T) {
	if _, err := os.Stat("/dev/full"); err != nil {
		t.Skip("/dev/full not available")
	}
	err := writeInstallLogLine("/dev/full", "install 1.0.0 -> 1.0.1 ok source=test")
	assert.Error(t, err, "a write to a full device must be reported")
}

// A good append succeeds and writes exactly the line.
func TestWriteInstallLogLineAppends(t *testing.T) {
	path := filepath.Join(t.TempDir(), "auto-update.log")
	assert.NoError(t, writeInstallLogLine(path, "first"))
	assert.NoError(t, writeInstallLogLine(path, "second"))
	data, err := os.ReadFile(path)
	assert.NoError(t, err)
	assert.Equal(t, "first\nsecond\n", string(data))
}
