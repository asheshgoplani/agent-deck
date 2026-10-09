package core

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"github.com/asheshgoplani/agent-deck/internal/session"
	"github.com/asheshgoplani/agent-deck/internal/telemetry"
)

// enableTelemetryForTest grants consent in an isolated HOME so events land in
// the local spool. Nothing is uploaded: no project key is configured.
func enableTelemetryForTest(t *testing.T) {
	t.Helper()
	telemetry.EnableForTest(t)
	telemetry.SetTerminalForTest(t, true)
	telemetry.ClearCIForTest(t)
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_DATA_HOME", filepath.Join(home, "data"))
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, "config"))
	t.Setenv("XDG_CACHE_HOME", filepath.Join(home, "cache"))
	for _, k := range []string{telemetry.EnvTelemetry, telemetry.EnvDoNotTrack, "AGENTDECK_INSTANCE_ID", "AGENT_DECK_SESSION_ID", "CLAUDECODE", "GEMINI_CLI", "CURSOR_AGENT", "CODEX_SANDBOX", "CODEX_THREAD_ID"} {
		t.Setenv(k, "")
		os.Unsetenv(k) // set-but-empty AGENTDECK_TELEMETRY is a hard off
	}
	telemetry.SetConfigDisabled(false)
	s := telemetry.LoadState()
	if err := telemetry.Grant(s, "test", time.Now()); err != nil {
		t.Fatal(err)
	}
	if err := telemetry.SaveState(s); err != nil {
		t.Fatal(err)
	}
}

// spooledEvents returns the spooled events named name.
func spooledEvents(t *testing.T, name string) []map[string]any {
	t.Helper()
	p, err := telemetry.StatePath()
	if err != nil {
		t.Fatal(err)
	}
	f, err := os.Open(filepath.Join(filepath.Dir(p), telemetry.SpoolFileName))
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	var out []map[string]any
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		var line struct {
			E string         `json:"e"`
			P map[string]any `json:"p"`
		}
		if err := json.Unmarshal(sc.Bytes(), &line); err != nil {
			t.Fatalf("bad spool line %q: %v", sc.Text(), err)
		}
		if line.E == name {
			out = append(out, line.P)
		}
	}
	return out
}

// session.stop through the core registry is the default CLI path
// (`agent-deck session stop`) and the daemon route; it must record exactly
// one session.end with end_kind=stop, like the TUI, web and legacy CLI paths.
func TestSessionStopRecordsTelemetryEnd(t *testing.T) {
	if _, err := exec.LookPath("tmux"); err != nil {
		t.Skip("tmux not available")
	}
	enableTelemetryForTest(t)

	profile := fmt.Sprintf("_core_stop_telemetry_%d", time.Now().UnixNano())
	inst := session.NewInstance(fmt.Sprintf("stoptel-%d", time.Now().UnixNano()), t.TempDir())
	inst.Command = ""
	if err := inst.Start(); err != nil {
		t.Fatalf("Start: %v", err)
	}
	t.Cleanup(func() { _ = inst.Kill() })
	deadline := time.Now().Add(3 * time.Second)
	for !inst.Exists() && time.Now().Before(deadline) {
		time.Sleep(50 * time.Millisecond)
	}
	if !inst.Exists() {
		t.Fatal("tmux session never appeared")
	}
	seedStore(t, profile, nil, inst)
	if n := len(spooledEvents(t, "session.end")); n != 0 {
		t.Fatalf("session.end spooled before stop: %d", n)
	}

	_, res := Invoke[SessionStopOut](context.Background(), testRegistry(t, Deps{}), IDSessionStop, SessionStopIn{Profile: profile, Session: inst.Title})
	if res.Err != nil {
		t.Fatal(res.Err)
	}
	res.Finish()

	ends := spooledEvents(t, "session.end")
	if len(ends) != 1 {
		t.Fatalf("session.end events after core session.stop = %d, want 1", len(ends))
	}
	if got := ends[0]["end_kind"]; got != string(telemetry.EndStop) {
		t.Fatalf("end_kind = %v, want %q", got, telemetry.EndStop)
	}
}
