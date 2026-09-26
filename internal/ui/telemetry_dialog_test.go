package ui

import (
	"errors"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/asheshgoplani/agent-deck/internal/telemetry"
	tea "github.com/charmbracelet/bubbletea"
)

type dialogHarness struct {
	d      *TelemetryDialog
	st     *telemetry.State
	saves  *[]telemetry.State
	grants *int
}

func telemetryDialogHarness(t *testing.T) dialogHarness {
	t.Helper()
	telemetry.EnableForTest(t)
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_DATA_HOME", home+"/data")
	t.Setenv("XDG_CONFIG_HOME", home+"/config")
	t.Setenv("XDG_CACHE_HOME", home+"/cache")
	t.Setenv(telemetry.EnvTelemetry, "")
	os.Unsetenv(telemetry.EnvTelemetry) // set-but-empty is a hard off
	t.Setenv(telemetry.EnvDoNotTrack, "")
	t.Setenv("CI", "")
	for _, k := range []string{"AGENTDECK_INSTANCE_ID", "AGENT_DECK_SESSION_ID", "CLAUDECODE", "GEMINI_CLI", "CURSOR_AGENT", "CODEX_SANDBOX", "CODEX_THREAD_ID"} {
		t.Setenv(k, "")
	}
	telemetry.SetConfigDisabled(false)

	saves := &[]telemetry.State{}
	grants := new(int)
	d := NewTelemetryDialog()
	d.SetSize(80, 24)
	d.canConsent = func() bool { return true }
	d.endpoint = telemetry.Endpoint()
	d.saveState = func(s *telemetry.State) error {
		*saves = append(*saves, *s)
		return nil
	}
	d.declineState = d.saveState
	d.now = func() time.Time { return time.Date(2026, 9, 26, 12, 0, 0, 0, time.UTC) }
	st := &telemetry.State{SchemaVersion: telemetry.SchemaVersion, Consent: telemetry.ConsentUndecided}
	return dialogHarness{d: d, st: st, saves: saves, grants: grants}
}

func key(s string) tea.KeyMsg {
	switch s {
	case "esc":
		return tea.KeyMsg{Type: tea.KeyEsc}
	case "enter":
		return tea.KeyMsg{Type: tea.KeyEnter}
	case "tab":
		return tea.KeyMsg{Type: tea.KeyTab}
	case "shift+tab":
		return tea.KeyMsg{Type: tea.KeyShiftTab}
	case "left":
		return tea.KeyMsg{Type: tea.KeyLeft}
	case "right":
		return tea.KeyMsg{Type: tea.KeyRight}
	case "ctrl+c":
		return tea.KeyMsg{Type: tea.KeyCtrlC}
	}
	return tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(s)}
}

func forceShow(h dialogHarness) {
	h.d.open("9.9.9", h.st, telemetry.SourceTUIFirstRun)
	h.d.shownAt = h.d.now().Add(-time.Second)
}

// grantedMsgs runs a returned command batch and counts telemetryGrantedMsg.
func grantedMsgs(cmd tea.Cmd) int {
	if cmd == nil {
		return 0
	}
	switch m := cmd().(type) {
	case telemetryGrantedMsg:
		return 1
	case tea.BatchMsg:
		n := 0
		for _, c := range m {
			if c == nil {
				continue
			}
			if _, ok := c().(telemetryGrantedMsg); ok {
				n++
			}
		}
		return n
	}
	return 0
}

func TestTelemetryDialogRespectsShouldPrompt(t *testing.T) {
	h := telemetryDialogHarness(t)
	h.st.Consent = telemetry.ConsentDeclined
	h.st.DeclinedSchema = telemetry.SchemaVersion
	if h.d.Show("9.9.9", h.st) || h.d.IsVisible() {
		t.Fatal("a schema 2 decline must never be asked again")
	}
	t.Setenv(telemetry.EnvDoNotTrack, "1")
	h.st.Consent = telemetry.ConsentUndecided
	if h.d.Show("9.9.9", h.st) {
		t.Fatal("DO_NOT_TRACK must suppress the prompt")
	}
}

func TestTelemetryDialogEnterOnFocusedAcceptGrantsAfterDurableSave(t *testing.T) {
	h := telemetryDialogHarness(t)
	forceShow(h)
	_, cmd := h.d.Update(key("enter"))
	if h.st.Consent != telemetry.ConsentGranted || len(*h.saves) != 1 || (*h.saves)[0].Consent != telemetry.ConsentGranted {
		t.Fatal("Enter on the focused Accept must grant and save")
	}
	if grantedMsgs(cmd) != 1 {
		t.Fatal("consent events must be recorded only via the post-save message")
	}
	if h.d.step != telemetryStepGranted || !strings.Contains(h.d.View(), "Sharing is on. Nothing is sent before tomorrow.") {
		t.Fatal("confirmation line")
	}
}

func TestTelemetryDialogYAccepts(t *testing.T) {
	h := telemetryDialogHarness(t)
	forceShow(h)
	h.d.Update(key("y"))
	if h.st.Consent != telemetry.ConsentGranted {
		t.Fatal("y must accept")
	}
}

func TestTelemetryDialogFocusMovesThenEnterDeclines(t *testing.T) {
	for _, k := range []string{"tab", "shift+tab", "right", "left", "l", "h"} {
		t.Run(k, func(t *testing.T) {
			h := telemetryDialogHarness(t)
			forceShow(h)
			h.d.Update(key(k))
			if h.d.focus != telemetryFocusNo {
				t.Fatalf("%s did not move focus", k)
			}
			_, cmd := h.d.Update(key("enter"))
			if h.st.Consent != telemetry.ConsentDeclined || grantedMsgs(cmd) != 0 {
				t.Fatal("Enter on the focused No must decline")
			}
		})
	}
}

func TestTelemetryDialogNAndEscDeclineRemembered(t *testing.T) {
	for _, k := range []string{"n", "N", "esc"} {
		t.Run(k, func(t *testing.T) {
			h := telemetryDialogHarness(t)
			forceShow(h)
			_, cmd := h.d.Update(key(k))
			if h.st.Consent != telemetry.ConsentDeclined || h.st.DeclinedSchema != telemetry.SchemaVersion || grantedMsgs(cmd) != 0 {
				t.Fatal("must decline and remember")
			}
			if !strings.Contains(h.d.View(), "You will not be asked again") {
				t.Fatal("decline line")
			}
		})
	}
}

func TestTelemetryDialogCtrlCPostponesWithoutQuitting(t *testing.T) {
	h := telemetryDialogHarness(t)
	forceShow(h)
	_, cmd := h.d.Update(key("ctrl+c"))
	if cmd != nil {
		t.Fatal("Ctrl-C must not return a command (it must not quit the TUI)")
	}
	if h.d.IsVisible() || h.st.Consent != telemetry.ConsentUndecided || len(*h.saves) != 0 {
		t.Fatal("Ctrl-C must close and leave the state undecided")
	}
	if h.st.DeclinedSchema != 0 || h.st.V1Declined() {
		t.Fatal("a postponed question must not be recorded as answered")
	}
}

func TestTelemetryDialogIgnoresOtherKeysAndPastes(t *testing.T) {
	h := telemetryDialogHarness(t)
	forceShow(h)
	for _, k := range []string{"q", "x", " ", "1", "?"} {
		h.d.Update(key(k))
	}
	h.d.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("y"), Paste: true})
	h.d.Update(tea.KeyMsg{Type: tea.KeyEnter, Paste: true})
	if h.st.Consent != telemetry.ConsentUndecided || len(*h.saves) != 0 || !h.d.IsVisible() {
		t.Fatal("other keys and pasted input must be ignored")
	}
}

func TestTelemetryDialogIgnoresKeysInsideGraceWindow(t *testing.T) {
	h := telemetryDialogHarness(t)
	forceShow(h)
	h.d.shownAt = h.d.now().Add(-telemetryKeyGrace + time.Millisecond)
	for _, k := range []string{"enter", "y", "n", "esc", "ctrl+c"} {
		h.d.Update(key(k))
	}
	if h.st.Consent != telemetry.ConsentUndecided || !h.d.IsVisible() {
		t.Fatal("keys inside 750 ms must be ignored")
	}
}

func TestTelemetryDialogTooSmallRefusesAccept(t *testing.T) {
	h := telemetryDialogHarness(t)
	h.d.SetSize(77, 24)
	forceShow(h)
	if !strings.Contains(h.d.View(), "Enlarge the window to 78×22") {
		t.Fatal("too-small notice")
	}
	for _, k := range []string{"enter", "y", "tab"} {
		h.d.Update(key(k))
	}
	if h.st.Consent != telemetry.ConsentUndecided {
		t.Fatal("accepted a question the user could not read")
	}
	h.d.Update(key("n"))
	if h.st.Consent != telemetry.ConsentDeclined {
		t.Fatal("n must still decline")
	}
	h2 := telemetryDialogHarness(t)
	h2.d.SetSize(80, 21)
	forceShow(h2)
	h2.d.Update(key("y"))
	if h2.st.Consent != telemetry.ConsentUndecided {
		t.Fatal("accepted below 22 rows")
	}
}

func TestTelemetryDialogFailedSaveIsNotConsent(t *testing.T) {
	h := telemetryDialogHarness(t)
	forceShow(h)
	h.d.saveState = func(*telemetry.State) error { return errors.New("disk full") }
	_, cmd := h.d.Update(key("enter"))
	if h.st.Consent == telemetry.ConsentGranted || h.st.InstallID != "" || grantedMsgs(cmd) != 0 {
		t.Fatal("unsaved consent must stay off")
	}
}

func TestTelemetryDialogChangedConditionsCannotGrant(t *testing.T) {
	h := telemetryDialogHarness(t)
	forceShow(h)
	h.d.canConsent = func() bool { return false }
	h.d.Update(key("enter"))
	if h.st.Consent == telemetry.ConsentGranted {
		t.Fatal("granted after a kill switch appeared")
	}
}

// An agent at a PTY must never be able to answer the question for a person:
// the real consent check refuses under every coding-agent marker.
func TestTelemetryDialogRefusesCodingAgents(t *testing.T) {
	telemetryDialogHarness(t)
	telemetry.SetTerminalForTest(t, true)
	if !NewTelemetryDialog().canConsent() {
		t.Fatal("a person at a terminal must be able to consent")
	}
	for _, marker := range []string{"CLAUDECODE", "GEMINI_CLI", "CURSOR_AGENT", "CODEX_SANDBOX", "CODEX_THREAD_ID"} {
		t.Run(marker, func(t *testing.T) {
			t.Setenv(marker, "1")
			d := NewTelemetryDialog()
			d.SetSize(80, 24)
			st := &telemetry.State{SchemaVersion: telemetry.SchemaVersion, Consent: telemetry.ConsentUndecided}
			if d.canConsent() || d.Show("9.9.9", st) || d.ShowFromSettings("9.9.9", st) {
				t.Fatalf("%s: the consent screen is available to a coding agent", marker)
			}
		})
	}
}

func TestTelemetryDialogV1DeclineAskedOnceWithNote(t *testing.T) {
	h := telemetryDialogHarness(t)
	h.st.SchemaVersion = 1
	h.st.Consent = telemetry.ConsentDeclined
	forceShow(h)
	if !strings.Contains(stripAnsi(h.d.View()), telemetry.PromptV1Declined) {
		t.Fatal("a v1 decliner must see the note")
	}
	h.d.Update(key("n"))
	if h.st.V1Declined() || telemetry.ShouldPrompt(h.st) {
		t.Fatal("after answering the v2 question a decline is final")
	}
}

func TestTelemetryDialogGoldenFrames(t *testing.T) {
	for _, tc := range []struct {
		name string
		prep func(h dialogHarness)
		w, h int
	}{
		{"ask", func(dialogHarness) {}, 80, 24},
		{"ask_no_focused", func(h dialogHarness) { h.d.focus = telemetryFocusNo }, 80, 24},
		{"ask_v1_declined", func(h dialogHarness) { h.d.v1Declined = true }, 80, 24},
		{"ask_min", func(dialogHarness) {}, 78, 22},
		{"too_small", func(dialogHarness) {}, 60, 15},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h := telemetryDialogHarness(t)
			forceShow(h)
			h.d.SetSize(tc.w, tc.h)
			tc.prep(h)
			frame := stripAnsi(h.d.View())
			if rows := strings.Count(strings.TrimRight(frame, "\n"), "\n") + 1; rows > tc.h {
				t.Fatalf("%d rows in a %d-row terminal", rows, tc.h)
			}
			if tc.name != "too_small" {
				for _, want := range []string{"Help improve agent-deck?", "More:   github.com/asheshgoplani/agent-deck/blob/main/TELEMETRY.md", "[ Share anonymous data ]", "[ No thanks ]", "Ctrl-C: ask me later"} {
					if !strings.Contains(frame, want) {
						t.Fatalf("frame lacks %q:\n%s", want, frame)
					}
				}
			}
			assertFrameGolden(t, "telemetry_consent_"+tc.name+".golden", frame)
		})
	}
}

func TestTelemetryDialogConsumesMouse(t *testing.T) {
	h := telemetryDialogHarness(t)
	forceShow(h)
	// A minimal Home proves mouse dispatch never reaches underlying panels.
	home := &Home{telemetryDialog: h.d}
	for _, button := range []tea.MouseButton{tea.MouseButtonWheelUp, tea.MouseButtonWheelDown, tea.MouseButtonLeft} {
		_, cmd := home.updateInner(tea.MouseMsg{Button: button})
		if cmd != nil || !h.d.visible || len(*h.saves) != 0 {
			t.Fatal("mouse escaped consent modal")
		}
	}
}
