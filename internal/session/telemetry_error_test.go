package session

import (
	"bufio"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/asheshgoplani/agent-deck/internal/telemetry"
)

type sessSpooled struct {
	E string         `json:"e"`
	P map[string]any `json:"p"`
}

// grantTelemetryForTest isolates HOME, pins a terminal and grants consent,
// so recorded events land in the test's spool.
func grantTelemetryForTest(t *testing.T) {
	t.Helper()
	telemetry.EnableForTest(t)
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_DATA_HOME", filepath.Join(home, "data"))
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, "config"))
	t.Setenv("XDG_CACHE_HOME", filepath.Join(home, "cache"))
	for _, k := range []string{telemetry.EnvTelemetry, telemetry.EnvDoNotTrack, telemetry.EnvPostHogKey} {
		t.Setenv(k, "")
		os.Unsetenv(k)
	}
	telemetry.ClearCIForTest(t)
	telemetry.SetConfigDisabled(false)
	telemetry.SetTerminalForTest(t, true)
	st := telemetry.LoadState()
	if err := telemetry.Grant(st, "9.9.9", time.Now()); err != nil {
		t.Fatal(err)
	}
	if err := telemetry.SaveState(st); err != nil {
		t.Fatal(err)
	}
}

func spooledErrors(t *testing.T) []map[string]any {
	t.Helper()
	p, err := telemetry.StatePath()
	if err != nil {
		t.Fatal(err)
	}
	f, err := os.Open(filepath.Join(filepath.Dir(p), telemetry.SpoolFileName))
	if err != nil {
		return nil
	}
	defer f.Close()
	var out []map[string]any
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		var l sessSpooled
		if json.Unmarshal(sc.Bytes(), &l) == nil && l.E == "error" {
			out = append(out, l.P)
		}
	}
	return out
}

func wantOneError(t *testing.T, area, kind string) {
	t.Helper()
	errs := spooledErrors(t)
	if len(errs) != 1 {
		t.Fatalf("spooled %d error events, want 1: %v", len(errs), errs)
	}
	if errs[0]["area"] != area || errs[0]["kind"] != kind {
		t.Fatalf("error event = %v, want area=%s kind=%s", errs[0], area, kind)
	}
}

// A session start that fails reports error area=session_start.
func TestSessionStartFailureRecordsError(t *testing.T) {
	grantTelemetryForTest(t)
	inst := &Instance{ID: "telemetry-start-fail", Title: "t", Tool: "claude"}
	if err := inst.Start(); err == nil {
		t.Fatal("Start without a tmux session succeeded")
	}
	wantOneError(t, "session_start", "other")
}

// StartWithMessage reports the same failure class.
func TestSessionStartWithMessageFailureRecordsError(t *testing.T) {
	grantTelemetryForTest(t)
	inst := &Instance{ID: "telemetry-startmsg-fail", Title: "t", Tool: "claude"}
	if err := inst.StartWithMessage("hi"); err == nil {
		t.Fatal("StartWithMessage without a tmux session succeeded")
	}
	wantOneError(t, "session_start", "other")
}

// A failed MCP attach/detach write reports error area=mcp.
func TestMCPWriteFailureRecordsError(t *testing.T) {
	grantTelemetryForTest(t)
	inst := &Instance{ID: "telemetry-mcp-fail", Title: "t", Tool: "shell", ProjectPath: t.TempDir()}
	if err := inst.WriteLocalMCPConfig([]string{"github"}); err == nil {
		t.Fatal("WriteLocalMCPConfig for an unsupported tool succeeded")
	}
	wantOneError(t, "mcp", "other")
}
