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

type uiSpooled struct {
	E string         `json:"e"`
	P map[string]any `json:"p"`
}

func uiSpool(t *testing.T) []uiSpooled {
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
	var out []uiSpooled
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		var l uiSpooled
		if json.Unmarshal(sc.Bytes(), &l) == nil {
			out = append(out, l)
		}
	}
	return out
}

// A TUI whose program loop ends in a panic records error kind=panic
// (area tui) as well as app.exit kind=panic.
func TestTUIPanicExitRecordsPanicError(t *testing.T) {
	h := telemetryDialogHarness(t)
	telemetry.SetTerminalForTest(t, true)
	if err := telemetry.Grant(h.st, "9.9.9", time.Now()); err != nil {
		t.Fatal(err)
	}
	if err := telemetry.SaveState(h.st); err != nil {
		t.Fatal(err)
	}
	home := &Home{}
	home.tel.started = true
	home.tel.startedAt = time.Now().Add(-time.Minute)
	home.CloseTelemetry(telemetry.ExitPanic)

	var gotErr, gotExit bool
	for _, l := range uiSpool(t) {
		switch l.E {
		case "error":
			if l.P["area"] == "tui" && l.P["kind"] == "panic" {
				gotErr = true
			}
		case "app.exit":
			if l.P["exit_kind"] == "panic" {
				gotExit = true
			}
		}
	}
	if !gotExit {
		t.Fatal("no app.exit exit_kind=panic spooled")
	}
	if !gotErr {
		t.Fatal("a TUI panic spooled no error event with area=tui kind=panic")
	}
}

// A normal quit records no error event.
func TestTUIQuitRecordsNoError(t *testing.T) {
	h := telemetryDialogHarness(t)
	telemetry.SetTerminalForTest(t, true)
	if err := telemetry.Grant(h.st, "9.9.9", time.Now()); err != nil {
		t.Fatal(err)
	}
	if err := telemetry.SaveState(h.st); err != nil {
		t.Fatal(err)
	}
	home := &Home{}
	home.tel.started = true
	home.tel.startedAt = time.Now().Add(-time.Minute)
	home.CloseTelemetry(telemetry.ExitQuit)
	for _, l := range uiSpool(t) {
		if l.E == "error" {
			t.Fatalf("quit spooled an error event: %v", l.P)
		}
	}
}
