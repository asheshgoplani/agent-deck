package ui

import (
	"bufio"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/asheshgoplani/agent-deck/internal/telemetry"
)

// A web stop or restart is a web event, like a web close or delete: the
// session.end it records must carry sf=web whatever process served the
// request. Before this was pinned, a web stop recorded nothing and a web
// restart was labelled with the process surface (cli for `web --no-tui`,
// tui inside the TUI).

type spooledEnd struct {
	Surface string
	Kind    string
}

// grantTelemetryForTest isolates HOME, lifts every hard-off for this test,
// pins the process surface and grants consent, so session.end lands in the
// spool. It returns the spool path.
func grantTelemetryForTest(t *testing.T, process telemetry.Surface) string {
	t.Helper()
	telemetry.EnableForTest(t)
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_DATA_HOME", home+"/data")
	t.Setenv("XDG_CONFIG_HOME", home+"/config")
	t.Setenv("XDG_CACHE_HOME", home+"/cache")
	for _, k := range []string{telemetry.EnvTelemetry, telemetry.EnvDoNotTrack} {
		t.Setenv(k, "")
		os.Unsetenv(k) // set-but-empty is a hard off
	}
	telemetry.ClearCIForTest(t)
	for _, k := range []string{"AGENTDECK_INSTANCE_ID", "AGENT_DECK_SESSION_ID", "CLAUDECODE", "GEMINI_CLI", "CURSOR_AGENT", "CODEX_SANDBOX", "CODEX_THREAD_ID", "TMUX", "TMUX_PANE"} {
		t.Setenv(k, "")
		os.Unsetenv(k)
	}
	telemetry.SetTerminalForTest(t, true)
	telemetry.SetConfigDisabled(false)
	telemetry.SetProcess("9.9.9", process)
	t.Cleanup(func() { telemetry.SetProcess("dev", telemetry.SurfaceCLI) })

	st := telemetry.LoadState()
	if err := telemetry.Grant(st, "9.9.9", time.Now()); err != nil {
		t.Fatalf("Grant: %v", err)
	}
	if err := telemetry.SaveState(st); err != nil {
		t.Fatalf("SaveState: %v", err)
	}
	statePath, err := telemetry.StatePath()
	if err != nil {
		t.Fatalf("StatePath: %v", err)
	}
	return filepath.Join(filepath.Dir(statePath), telemetry.SpoolFileName)
}

func spooledSessionEnds(t *testing.T, path string) []spooledEnd {
	t.Helper()
	f, err := os.Open(path)
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		t.Fatalf("open spool: %v", err)
	}
	defer f.Close()
	var out []spooledEnd
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		var l struct {
			E  string         `json:"e"`
			SF string         `json:"sf"`
			P  map[string]any `json:"p"`
		}
		if err := json.Unmarshal(sc.Bytes(), &l); err != nil {
			t.Fatalf("spool line %q: %v", sc.Text(), err)
		}
		if l.E != "session.end" {
			continue
		}
		kind, _ := l.P["end_kind"].(string)
		out = append(out, spooledEnd{Surface: l.SF, Kind: kind})
	}
	return out
}

func TestWebMutatorStopAndRestartRecordWebSessionEnd(t *testing.T) {
	skipIfNoTmuxBinaryUI(t)
	// The process is the TUI, so a web request served by it must still be
	// labelled web, while the TUI's own restart keeps the process surface.
	spool := grantTelemetryForTest(t, telemetry.SurfaceTUI)

	home, _ := newRestartSaveHome(t, "_websessionendtelemetry")
	target := home.instances[1]
	t.Cleanup(func() {
		if sess := target.GetTmuxSession(); sess != nil {
			_ = sess.Kill()
		}
	})
	m := &WebMutator{h: home}

	if err := m.RestartSession(target.ID); err != nil {
		t.Fatalf("web RestartSession: %v", err)
	}
	if err := target.Restart(); err != nil {
		t.Fatalf("TUI Restart: %v", err)
	}
	if err := m.StopSession(target.ID); err != nil {
		t.Fatalf("web StopSession: %v", err)
	}

	got := spooledSessionEnds(t, spool)
	want := []spooledEnd{
		{Surface: "web", Kind: string(telemetry.EndRestart)},
		{Surface: "tui", Kind: string(telemetry.EndRestart)},
		{Surface: "web", Kind: string(telemetry.EndStop)},
	}
	if len(got) != len(want) {
		t.Fatalf("session.end rows = %+v, want %+v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("session.end[%d] = %+v, want %+v (all rows: %+v)", i, got[i], want[i], got)
		}
	}
}
