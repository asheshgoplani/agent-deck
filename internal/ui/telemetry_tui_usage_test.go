package ui

import (
	"testing"
	"time"

	"github.com/asheshgoplani/agent-deck/internal/session"
	"github.com/asheshgoplani/agent-deck/internal/telemetry"
	tea "github.com/charmbracelet/bubbletea"
)

// TUI usage must reach the same daily rollups as the CLI: a message typed in
// insert mode is a send.daily via=tui, and TUI renames, group creates and
// moves are feature.daily entries. Before this, TUI feature.daily held only
// "fork" and TUI sends were never counted.

// grantedTelemetry isolates HOME, grants consent and pins a TTY so the
// telemetry package records.
func grantedTelemetry(t *testing.T) {
	t.Helper()
	h := telemetryDialogHarness(t)
	telemetry.SetTerminalForTest(t, true)
	if err := telemetry.Grant(h.st, "9.9.9", time.Now()); err != nil {
		t.Fatal(err)
	}
	if err := telemetry.SaveState(h.st); err != nil {
		t.Fatal(err)
	}
}

// todayRollup returns today's local rollup from the saved state.
func todayRollup(t *testing.T) *telemetry.DailyRollup {
	t.Helper()
	r := telemetry.LoadState().Daily[time.Now().Local().Format(telemetry.DayFormat)]
	if r == nil {
		return &telemetry.DailyRollup{}
	}
	return r
}

// runTelemetryCmd runs a returned command synchronously (nil is allowed).
func runTelemetryCmd(cmd tea.Cmd) {
	if cmd == nil {
		return
	}
	if batch, ok := cmd().(tea.BatchMsg); ok {
		for _, c := range batch {
			runTelemetryCmd(c)
		}
	}
}

func TestTelemetryTUIInsertModeSendCountsSendDaily(t *testing.T) {
	grantedTelemetry(t)
	home, _, capture := armHomeWithOneSession(t)

	model, _ := home.handleMainKey(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'I'}})
	home = model.(*Home)
	if !home.insertMode {
		t.Fatal("not in insert mode")
	}
	for _, r := range "hello" {
		model, _ = home.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{r}})
		home = model.(*Home)
	}
	model, cmd := home.Update(tea.KeyMsg{Type: tea.KeyEnter})
	home = model.(*Home)
	runTelemetryCmd(cmd)

	// A bare Enter (menu confirmation, nothing typed) is not a message.
	_, cmd = home.Update(tea.KeyMsg{Type: tea.KeyEnter})
	runTelemetryCmd(cmd)
	if len(capture.calls) == 0 {
		t.Fatal("harness: nothing reached the session")
	}

	r := todayRollup(t)
	c := r.SendsBy["claude|tui"]
	if c == nil || c.Count != 1 || r.Sends != 1 {
		t.Fatalf("send.daily via=tui not counted once: sends=%d by=%v", r.Sends, r.SendsBy)
	}
	if c.Len["<50"] != 1 || c.Queued != 0 {
		t.Fatalf("len bucket/queued wrong: %+v", c)
	}
}

func TestTelemetryTUIRenameAndGroupOpsCountFeatureDaily(t *testing.T) {
	grantedTelemetry(t)
	home := NewHome()
	home.width, home.height = 100, 30
	inst := session.NewInstance("original-name", "/tmp/project")
	home.instancesMu.Lock()
	home.instances = []*session.Instance{inst}
	home.instanceByID[inst.ID] = inst
	home.instancesMu.Unlock()
	home.groupTree = session.NewGroupTree(home.instances)
	home.rebuildFlatItems()
	for i, item := range home.flatItems {
		if item.Type == session.ItemTypeSession {
			home.cursor = i
		}
	}

	submit := func() {
		t.Helper()
		_, cmd := home.Update(tea.KeyMsg{Type: tea.KeyEnter})
		runTelemetryCmd(cmd)
		if home.groupDialog.IsVisible() {
			t.Fatal("dialog still open")
		}
	}

	home.groupDialog.ShowRenameSession(inst.ID, inst.Title)
	home.groupDialog.nameInput.SetValue("new-name")
	submit()
	if inst.Title != "new-name" {
		t.Fatalf("harness: rename did not apply (%q)", inst.Title)
	}

	home.groupDialog.ShowCreateWithContext("", "")
	home.groupDialog.nameInput.SetValue("team")
	submit()

	for i, item := range home.flatItems {
		if item.Type == session.ItemTypeSession && item.Session == inst {
			home.cursor = i
		}
	}
	home.groupDialog.ShowMove([]string{"team"})
	submit()
	if inst.GroupPath != "team" {
		t.Fatalf("harness: move did not apply (%q)", inst.GroupPath)
	}

	got := todayRollup(t).Features
	for _, f := range []string{"rename", "group_create", "move_group"} {
		if got[f] == nil || got[f].Count != 1 || got[f].Errors != 0 {
			t.Errorf("feature.daily %s = %+v, want count 1", f, got[f])
		}
	}
}
