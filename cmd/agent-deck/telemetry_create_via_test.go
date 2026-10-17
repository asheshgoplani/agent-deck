package main

import (
	"bufio"
	"encoding/json"
	"os"
	"path/filepath"
	"regexp"
	"testing"

	"github.com/asheshgoplani/agent-deck/internal/telemetry"
)

// spooledCreateVias returns the via of every spooled session.create.
func spooledCreateVias(t *testing.T) []string {
	t.Helper()
	sp, err := telemetry.StatePath()
	if err != nil {
		t.Fatal(err)
	}
	f, err := os.Open(filepath.Join(filepath.Dir(sp), telemetry.SpoolFileName))
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	var vias []string
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		var line struct {
			E string         `json:"e"`
			P map[string]any `json:"p"`
		}
		if err := json.Unmarshal(sc.Bytes(), &line); err != nil {
			t.Fatalf("spool line: %v", err)
		}
		if line.E == "session.create" {
			v, _ := line.P["via"].(string)
			vias = append(vias, v)
		}
	}
	return vias
}

// Plain `agent-deck add` (no --attach) creates a session and must record
// exactly one session.create via=cli_add, like every other create surface.
// Before the fix only `add --attach` recorded it, and the later
// `session start` has no create hook, so add+start was never counted.
func TestHandleAddRecordsSessionCreate(t *testing.T) {
	isolateTelemetryHome(t)
	_, _, profile := setupAddDefaultPathTest(t)
	telemetry.SetTerminalForTest(t, true)
	if code, out, _ := runTel(t, "y\n", true, "on"); code != 0 || !statusJSON(t).Enabled {
		t.Fatalf("consent not granted: %d %s", code, out)
	}

	captureStdout(t, func() {
		handleAdd(profile, []string{"--title", "tel-add", "-c", "bash", "--json"})
	})

	if got := spooledCreateVias(t); len(got) != 1 || got[0] != string(telemetry.ViaCLIAdd) {
		t.Fatalf("plain add spooled session.create vias %v, want exactly [cli_add]", got)
	}
}

// `add --attach` starts the session and must still record exactly once, and
// `session fork` must not be labelled cli_add. Both paths need tmux and a
// TTY, so pin the call sites in source.
func TestCLICreateHookCallSites(t *testing.T) {
	src := func(name string) string {
		b, err := os.ReadFile(name)
		if err != nil {
			t.Fatal(err)
		}
		return string(b)
	}
	main := src("main.go")
	if n := len(regexp.MustCompile(`RecordTelemetryCreate\(telemetry\.ViaCLIAdd\)`).FindAllStringIndex(main, -1)); n != 2 {
		t.Fatalf("main.go has %d cli_add create hooks, want 2 (add --attach after Start, plain add after registration)", n)
	}
	fork := src("session_cmd.go")
	if regexp.MustCompile(`forkedInst\.RecordTelemetryCreate\(telemetry\.ViaCLIAdd\)`).MatchString(fork) {
		t.Fatal("session fork records via=cli_add; forks must not be counted as adds")
	}
	if !regexp.MustCompile(`forkedInst\.RecordTelemetryCreate\(telemetry\.ViaCLIFork\)`).MatchString(fork) {
		t.Fatal("session fork must record via=cli_fork")
	}
}
