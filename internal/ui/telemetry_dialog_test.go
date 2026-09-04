package ui

import (
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/asheshgoplani/agent-deck/internal/telemetry"
	tea "github.com/charmbracelet/bubbletea"
)

func telemetryDialogHarness(t *testing.T) (*TelemetryDialog, *telemetry.State, *[]telemetry.State, *int) {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_DATA_HOME", home+"/data")
	t.Setenv("XDG_CONFIG_HOME", home+"/config")
	t.Setenv("XDG_CACHE_HOME", home+"/cache")
	t.Setenv(telemetry.EnvTelemetry, "")
	t.Setenv(telemetry.EnvDoNotTrack, "")
	t.Setenv("CI", "")
	t.Setenv("AGENTDECK_INSTANCE_ID", "")
	telemetry.SetConfigDisabled(false)

	saves := &[]telemetry.State{}
	sends := new(int)
	d := NewTelemetryDialog()
	d.SetSize(120, 60)
	d.canConsent = func() bool { return true }
	d.endpoint = telemetry.Endpoint()
	d.saveState = func(s *telemetry.State) error {
		*saves = append(*saves, *s)
		return nil
	}
	d.declineState = d.saveState
	d.sendCmd = func(string) tea.Cmd {
		*sends++
		return nil
	}
	d.now = func() time.Time { return time.Date(2026, 9, 4, 12, 0, 0, 0, time.UTC) }
	st := &telemetry.State{SchemaVersion: telemetry.SchemaVersion, Consent: telemetry.ConsentUndecided}
	return d, st, saves, sends
}

func key(s string) tea.KeyMsg {
	switch s {
	case "esc":
		return tea.KeyMsg{Type: tea.KeyEsc}
	case "enter":
		return tea.KeyMsg{Type: tea.KeyEnter}
	}
	return tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(s)}
}

func forceShow(d *TelemetryDialog, st *telemetry.State) {
	d.visible = true
	d.step = telemetryStepAsk
	d.version = "9.9.9"
	d.state = st
	d.shownAt = d.now().Add(-time.Second)
}

func TestTelemetryDialogRespectsShouldPrompt(t *testing.T) {
	d, st, _, _ := telemetryDialogHarness(t)
	if d.Show("9.9.9", st) || d.IsVisible() {
		t.Fatal("dialog opened without an interactive terminal")
	}
	st.Consent = telemetry.ConsentDeclined
	if d.Show("9.9.9", st) {
		t.Fatal("dialog opened for a declined user")
	}
	if d.View() != "" {
		t.Fatal("hidden dialog rendered")
	}
}

func TestTelemetryDialogYesGrantsSavesThenSends(t *testing.T) {
	d, st, saves, sends := telemetryDialogHarness(t)
	forceShow(d, st)
	view := d.View()
	for _, want := range []string{"Help improve agent-deck?", telemetry.Endpoint(), telemetry.DocsURL, "[y]", "[n]"} {
		if !strings.Contains(view, want) {
			t.Errorf("ask view lacks %q", want)
		}
	}
	if *sends != 0 {
		t.Fatal("send scheduled before any answer")
	}
	_, cmd := d.Update(key("y"))
	if cmd == nil {
		t.Fatal("no command after y")
	}
	if len(*saves) != 1 || (*saves)[0].Consent != telemetry.ConsentGranted || len((*saves)[0].InstallID) != 32 {
		t.Fatalf("saves = %+v", *saves)
	}
	if (*saves)[0].ConsentDay != "2026-09-04" || (*saves)[0].ConsentVersion != "9.9.9" {
		t.Fatalf("consent record = %+v", (*saves)[0])
	}
	if *sends != 1 {
		t.Fatalf("sends = %d, want 1 after consent persisted", *sends)
	}
	if !strings.Contains(d.View(), "Thank you") {
		t.Fatalf("granted view = %q", d.View())
	}
}

func TestTelemetryDialogAnyOtherKeyDeclinesAndNeverSends(t *testing.T) {
	for _, k := range []string{"n", "N", "esc", "enter", "q", "j", " "} {
		t.Run(k, func(t *testing.T) {
			d, st, saves, sends := telemetryDialogHarness(t)
			forceShow(d, st)
			d.Update(key(k))
			if *sends != 0 {
				t.Fatal("send scheduled after a decline")
			}
			if len(*saves) != 1 || (*saves)[0].Consent != telemetry.ConsentDeclined || (*saves)[0].InstallID != "" {
				t.Fatalf("saves = %+v", *saves)
			}
			if !strings.Contains(d.View(), "stays off") || !strings.Contains(d.View(), "telemetry enable") {
				t.Fatalf("declined view = %q", d.View())
			}
		})
	}
}

func TestTelemetryDialogFailedSaveIsNotConsent(t *testing.T) {
	d, st, _, sends := telemetryDialogHarness(t)
	d.saveState = func(*telemetry.State) error { return errors.New("disk full") }
	forceShow(d, st)
	d.Update(key("y"))
	if *sends != 0 {
		t.Fatal("sent although consent was never persisted")
	}
	if st.Consent != telemetry.ConsentUndecided || st.InstallID != "" {
		t.Fatalf("state after failed save = %+v", st)
	}
	if !strings.Contains(d.View(), "could not save") {
		t.Fatalf("view = %q", d.View())
	}
}

func TestTelemetryDialogIgnoresKeysInsideGraceWindow(t *testing.T) {
	d, st, saves, sends := telemetryDialogHarness(t)
	forceShow(d, st)
	d.shownAt = d.now() // just appeared
	for _, k := range []string{"y", "n", "esc", "enter"} {
		d.Update(key(k))
	}
	if len(*saves) != 0 || *sends != 0 || d.step != telemetryStepAsk || !d.IsVisible() {
		t.Fatalf("a key inside the grace window was treated as an answer: saves=%d sends=%d step=%d", len(*saves), *sends, d.step)
	}
	d.shownAt = d.now().Add(-telemetryKeyGrace)
	d.Update(key("y"))
	if len(*saves) != 1 || (*saves)[0].Consent != telemetry.ConsentGranted || *sends != 1 {
		t.Fatalf("y after grace: saves=%+v sends=%d", *saves, *sends)
	}
}

func TestTelemetryDialogNoTimerAnswersForTheUser(t *testing.T) {
	d, st, saves, sends := telemetryDialogHarness(t)
	forceShow(d, st)
	if len(*saves) != 0 || *sends != 0 || d.step != telemetryStepAsk || !d.IsVisible() {
		t.Fatal("dialog acted without input")
	}
}

func TestTelemetryDialogClippedDisclosureCannotGrant(t *testing.T) {
	d, st, saves, sends := telemetryDialogHarness(t)
	forceShow(d, st)
	d.SetSize(80, 24)
	d.Update(key("y"))
	if len(*saves) != 0 || *sends != 0 {
		t.Fatal("granted with clipped disclosure")
	}
	if !strings.Contains(d.View(), "Telemetry is OFF") {
		t.Fatal(d.View())
	}
	d.Update(key("n"))
	if st.Consent != telemetry.ConsentDeclined {
		t.Fatal("cannot decline small dialog")
	}
}

func TestTelemetryDialogChangedConditionsCannotGrant(t *testing.T) {
	d, st, saves, sends := telemetryDialogHarness(t)
	forceShow(d, st)
	d.canConsent = func() bool { return false }
	d.Update(key("y"))
	if len(*saves) != 0 || *sends != 0 {
		t.Fatal("granted after conditions changed")
	}
}

func TestTelemetryDialogStaleDeclineOverridesConcurrentGrant(t *testing.T) {
	d, st, _, sends := telemetryDialogHarness(t)
	forceShow(d, st)
	concurrent := telemetry.LoadState()
	if err := telemetry.Grant(concurrent, "9.9.9", d.now()); err != nil {
		t.Fatal(err)
	}
	if err := telemetry.SaveState(concurrent); err != nil {
		t.Fatal(err)
	}
	d.declineState = NewTelemetryDialog().declineState
	d.Update(key("n"))
	if telemetry.LoadState().Consent != telemetry.ConsentDeclined || *sends != 0 || d.saveErr != nil {
		t.Fatal("stale decline did not win")
	}
}
