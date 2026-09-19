package update

import (
	"bytes"
	"encoding/json"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Every line of an unattended run lands in <cache>/update.log as well as
// the shared debug log, and every line says who ran it: trigger, pid, ppid,
// the launchd service it runs inside and the binary version. The 2026-09-19
// install left no trace in a debug.log four processes were rotating under
// each other; this file is appended, never rotated by rename.
func TestAuditLog_TeesEveryLineWithIdentity(t *testing.T) {
	dir := t.TempDir()
	var debug bytes.Buffer
	base := slog.New(slog.NewJSONHandler(&debug, nil))
	audit, closeFn, err := OpenAuditLog(dir, base, AuditIdentity{Trigger: "web", PID: 51055, PPID: 4242, Service: "com.agentdeck.web", Version: "1.16.11"})
	require.NoError(t, err)
	audit.Info("unattended_update_start", slog.String("current", "1.16.11"))
	audit.Warn("launchagent_self_deferred", slog.String("label", "com.agentdeck.web"))
	closeFn()

	data, err := os.ReadFile(filepath.Join(dir, AuditLogFileName))
	require.NoError(t, err)
	lines := strings.Split(strings.TrimSpace(string(data)), "\n")
	require.Len(t, lines, 2)
	for _, line := range lines {
		var got map[string]any
		require.NoError(t, json.Unmarshal([]byte(line), &got), line)
		assert.Equal(t, "web", got["trigger"])
		assert.Equal(t, float64(51055), got["pid"])
		assert.Equal(t, float64(4242), got["ppid"])
		assert.Equal(t, "com.agentdeck.web", got["service"])
		assert.Equal(t, "1.16.11", got["version"])
		assert.Equal(t, "update", got["component"])
	}
	assert.Contains(t, lines[0], `"msg":"unattended_update_start"`)
	assert.Contains(t, lines[1], `"level":"WARN"`)

	// The shared debug log got the same two lines with the same identity.
	assert.Equal(t, 2, strings.Count(debug.String(), "\n"))
	assert.Contains(t, debug.String(), `"trigger":"web"`)
	assert.Contains(t, debug.String(), `"pid":51055`)

	// A second run appends; nothing is lost.
	audit2, close2, err := OpenAuditLog(dir, base, AuditIdentity{Trigger: "tui", PID: 7, Version: "1.16.12"})
	require.NoError(t, err)
	audit2.Info("unattended_skipped", slog.String("reason", "current"))
	close2()
	data, _ = os.ReadFile(filepath.Join(dir, AuditLogFileName))
	assert.Equal(t, 3, strings.Count(string(data), "\n"))
	assert.Contains(t, string(data), `"trigger":"tui"`)
}

// An unwritable audit file never blocks the run: the debug log still gets
// the lines and the error is reported once.
func TestAuditLog_FallsBackToDebugLog(t *testing.T) {
	var debug bytes.Buffer
	base := slog.New(slog.NewJSONHandler(&debug, nil))
	audit, closeFn, err := OpenAuditLog(filepath.Join(t.TempDir(), "missing", "deeper"), base, AuditIdentity{Trigger: "timer", PID: 1, Version: "1.0.0"})
	require.NoError(t, err, "a missing dir is created")
	audit.Info("x")
	closeFn()
	assert.Contains(t, debug.String(), `"msg":"x"`)

	// A directory in the way of the file is the unwritable case.
	dir := t.TempDir()
	require.NoError(t, os.Mkdir(filepath.Join(dir, AuditLogFileName), 0o755))
	audit, closeFn, err = OpenAuditLog(dir, base, AuditIdentity{Trigger: "timer", PID: 1, Version: "1.0.0"})
	require.Error(t, err)
	require.NotNil(t, audit, "still usable: the debug log half")
	audit.Info("y")
	closeFn()
	assert.Contains(t, debug.String(), `"msg":"y"`)
}

// The file is capped by moving it aside once at open, never by a rename
// race between writers: a file over the cap becomes update.log.1.
func TestAuditLog_MovesAsideWhenOverCap(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, AuditLogFileName)
	require.NoError(t, os.WriteFile(path, bytes.Repeat([]byte("x"), auditLogCapBytes+1), 0o600))
	base := slog.New(slog.NewJSONHandler(&bytes.Buffer{}, nil))
	audit, closeFn, err := OpenAuditLog(dir, base, AuditIdentity{Trigger: "timer", PID: 1, Version: "1.0.0"})
	require.NoError(t, err)
	audit.Info("fresh")
	closeFn()
	data, err := os.ReadFile(path)
	require.NoError(t, err)
	assert.Equal(t, 1, strings.Count(string(data), "\n"))
	old, err := os.ReadFile(path + ".1")
	require.NoError(t, err)
	assert.Len(t, old, auditLogCapBytes+1)
}

func TestAuditIdentityFromEnv(t *testing.T) {
	id := auditIdentity("tui", "1.2.3", func(k string) string {
		if k == launchdServiceEnv {
			return "com.agentdeck.web"
		}
		return ""
	})
	assert.Equal(t, "tui", id.Trigger)
	assert.Equal(t, "1.2.3", id.Version)
	assert.Equal(t, "com.agentdeck.web", id.Service)
	assert.Equal(t, os.Getpid(), id.PID)
	assert.Equal(t, os.Getppid(), id.PPID)
}
