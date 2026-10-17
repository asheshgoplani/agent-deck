package ui

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/asheshgoplani/agent-deck/internal/telemetry"
	tea "github.com/charmbracelet/bubbletea"
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

// tuiExitEvents returns the spooled error events and app.exit kinds.
func tuiExitEvents(t *testing.T) (errs []map[string]any, exits []string) {
	t.Helper()
	for _, l := range uiSpool(t) {
		switch l.E {
		case "error":
			errs = append(errs, l.P)
		case "app.exit":
			exits = append(exits, l.P["exit_kind"].(string))
		}
	}
	return errs, exits
}

func grantedStartedHome(t *testing.T) *Home {
	t.Helper()
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
	return home
}

// bubbletea recovers a panic in Update, View or a Cmd and returns it from
// Run wrapped as "program was killed: program experienced a panic". That is
// recorded once as app.exit kind=panic plus error area=tui kind=panic, and
// a signal close racing behind it adds nothing.
func TestTUIRunPanicErrorRecordsPanicOnce(t *testing.T) {
	home := grantedStartedHome(t)
	home.CloseTelemetryAfterRun(fmt.Errorf("%w: %w", tea.ErrProgramKilled, tea.ErrProgramPanic))
	home.CloseTelemetry(telemetry.ExitSignal) // signal handler after the panic

	errs, exits := tuiExitEvents(t)
	if len(exits) != 1 || exits[0] != "panic" {
		t.Fatalf("app.exit kinds = %v, want [panic]", exits)
	}
	if len(errs) != 1 || errs[0]["area"] != "tui" || errs[0]["kind"] != "panic" {
		t.Fatalf("error events = %v, want one area=tui kind=panic", errs)
	}
}

// A SIGINT that bubbletea catches before agent-deck's own handler ends Run
// with ErrInterrupted. That is a signal exit, not a panic and not an error.
func TestTUIRunInterruptIsSignalNotPanic(t *testing.T) {
	home := grantedStartedHome(t)
	home.CloseTelemetryAfterRun(fmt.Errorf("%w: %w", tea.ErrProgramKilled, tea.ErrInterrupted))

	errs, exits := tuiExitEvents(t)
	if len(exits) != 1 || exits[0] != "signal" {
		t.Fatalf("app.exit kinds = %v, want [signal]", exits)
	}
	if len(errs) != 0 {
		t.Fatalf("an interrupt spooled error events %v, want none", errs)
	}
}

// Any other Run error (the TTY or terminal could not be set up) is a TUI
// failure classified by type, never kind=panic, and never app.exit panic.
func TestTUIRunSetupErrorIsNotPanic(t *testing.T) {
	home := grantedStartedHome(t)
	home.CloseTelemetryAfterRun(fmt.Errorf("could not open a new TTY: %w", os.ErrPermission))
	home.CloseTelemetry(telemetry.ExitSignal) // must not add a second record

	errs, exits := tuiExitEvents(t)
	if len(errs) != 1 || errs[0]["area"] != "tui" || errs[0]["kind"] != "permission" {
		t.Fatalf("error events = %v, want one area=tui kind=permission", errs)
	}
	for _, k := range exits {
		if k == "panic" || k == "signal" {
			t.Fatalf("a setup failure spooled app.exit kind=%s", k)
		}
	}
}

// A nil Run error is a normal quit: app.exit kind=quit and no error event.
func TestTUIRunNilErrorIsQuit(t *testing.T) {
	home := grantedStartedHome(t)
	home.CloseTelemetryAfterRun(nil)

	errs, exits := tuiExitEvents(t)
	if len(exits) != 1 || exits[0] != "quit" {
		t.Fatalf("app.exit kinds = %v, want [quit]", exits)
	}
	if len(errs) != 0 {
		t.Fatalf("a quit spooled error events %v", errs)
	}
}

// runEndModel ends a real bubbletea program from Init: with a panic or an
// interrupt, so the tests above see the exact error Run returns.
type runEndModel struct{ panics bool }

func (m runEndModel) Init() tea.Cmd {
	return func() tea.Msg { return runEndMsg{} }
}
func (m runEndModel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	if _, ok := msg.(runEndMsg); ok {
		if m.panics {
			panic("boom")
		}
		return m, tea.Interrupt
	}
	return m, nil
}
func (m runEndModel) View() string { return "" }

type runEndMsg struct{}

func realRunErr(t *testing.T, panics bool) error {
	t.Helper()
	p := tea.NewProgram(runEndModel{panics: panics}, tea.WithInput(nil), tea.WithOutput(io.Discard), tea.WithoutSignalHandler())
	_, err := p.Run()
	if err == nil {
		t.Fatal("program ended without an error")
	}
	return err
}

// The real error from a bubbletea panic is recorded as one panic, and the
// real error from an interrupt as a signal exit with no error event.
func TestTUIRunRealBubbleteaErrors(t *testing.T) {
	t.Run("panic", func(t *testing.T) {
		home := grantedStartedHome(t)
		home.CloseTelemetryAfterRun(realRunErr(t, true))
		errs, exits := tuiExitEvents(t)
		if len(exits) != 1 || exits[0] != "panic" || len(errs) != 1 || errs[0]["kind"] != "panic" || errs[0]["area"] != "tui" {
			t.Fatalf("exits=%v errors=%v, want one panic exit and one area=tui kind=panic", exits, errs)
		}
	})
	t.Run("interrupt", func(t *testing.T) {
		home := grantedStartedHome(t)
		home.CloseTelemetryAfterRun(realRunErr(t, false))
		errs, exits := tuiExitEvents(t)
		if len(exits) != 1 || exits[0] != "signal" || len(errs) != 0 {
			t.Fatalf("exits=%v errors=%v, want one signal exit and no error", exits, errs)
		}
	})
}
