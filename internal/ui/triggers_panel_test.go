package ui

// Seam A tests and golden frames for the Triggers panel (#2702). Regenerate
// the frames with:
//
//	UPDATE_GOLDEN=1 go test ./internal/ui/ -run 'TestTriggers.*Golden'
//
// and review the diff like any other test change.

import (
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/asheshgoplani/agent-deck/internal/session"
	"github.com/asheshgoplani/agent-deck/internal/statedb"
	"github.com/asheshgoplani/agent-deck/internal/triggers"
)

var triggersFixtureNow = time.Date(2026, 10, 18, 9, 0, 0, 0, time.UTC)

const (
	fixtureConductorID = "sess-conductor-0001"
	fixtureInboxID     = "sess-inbox-triage-01"
	fixtureMacOpsID    = "sess-mac-ops-000001"
)

// triggersFixtureSessions is the session list the fixture's targets resolve
// against: overnight-orchestrator is deliberately missing (a dead target).
func triggersFixtureSessions() []triggers.Resolved {
	return []triggers.Resolved{
		{SessionID: fixtureConductorID, Title: "conductor-ashesh"},
		{SessionID: fixtureInboxID, Title: "inbox-triage"},
		{SessionID: fixtureMacOpsID, Title: "mac-ops"},
	}
}

// openTriggersFixtureDB seeds a state DB with one trigger per health state
// across three kinds (launchd, systemd, watcher) plus a session-to-session
// sender that only shows with "all kinds":
//
//	agentdeck-overnight  systemd  dead target, 12 fires, 92% no-op
//	mail-poll            systemd  ok, 3 distinct fires
//	issue-poll-…         systemd  failing (last send failed), long name
//	com.ashesh.limit-…   launchd  never fired
//	github               watcher  ok but noisy (90% no-op)
//	worker-7             session  hidden by default
func openTriggersFixtureDB(t *testing.T) *statedb.StateDB {
	t.Helper()
	db, err := statedb.Open(filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	if err := db.Migrate(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	seedTriggersFixture(t, db, triggersFixtureNow)
	return db
}

func seedTriggersFixture(t *testing.T, db *statedb.StateDB, now time.Time) {
	t.Helper()
	const host = "deckbox"
	longUnit := "agentdeck-github-issue-poll-for-the-backend-team"
	if err := db.ReplaceScannedTriggers([]statedb.TriggerRow{
		{ID: "systemd:agentdeck-overnight", Kind: triggers.KindSystemd, Source: "agentdeck-overnight", Host: host,
			ConfigPath: "~/.config/systemd/user/agentdeck-overnight.service", Verb: "send",
			Target: "overnight-orchestrator", TargetBy: "title", Schedule: "every 5m", Unit: "agentdeck-overnight"},
		{ID: "systemd:mail-poll", Kind: triggers.KindSystemd, Source: "mail-poll", Host: host,
			ConfigPath: "~/.config/systemd/user/mail-poll.service", Verb: "send",
			Target: "inbox-triage", TargetBy: "title", Schedule: "every 10m", Unit: "mail-poll"},
		{ID: "systemd:" + longUnit, Kind: triggers.KindSystemd, Source: longUnit, Host: host,
			ConfigPath: "~/.config/systemd/user/" + longUnit + ".service", Verb: "send",
			Target: fixtureConductorID, TargetBy: "id", Schedule: "every 15m", Unit: longUnit},
		{ID: "launchd:com.ashesh.limit-guard", Kind: triggers.KindLaunchd, Source: "com.ashesh.limit-guard", Host: host,
			ConfigPath: "~/Library/LaunchAgents/com.ashesh.limit-guard.plist", Verb: "send",
			Target: "mac-ops", TargetBy: "title", Schedule: "every 1h", Unit: "com.ashesh.limit-guard"},
	}); err != nil {
		t.Fatal(err)
	}
	for _, row := range []statedb.TriggerRow{
		// The name carries an escape sequence: a sender's name is data, and
		// the panel must not let it repaint the screen.
		{ID: "watcher:github", Kind: triggers.KindWatcher, Source: "github\x1b[2J", Host: host, Verb: "send",
			Target: "conductor-ashesh", TargetBy: "title"},
		{ID: "session:worker-7", Kind: triggers.KindSession, Source: "worker-7", Host: host, Verb: "send",
			Target: "inbox-triage", TargetBy: "title"},
	} {
		if err := db.UpsertPushedTrigger(row); err != nil {
			t.Fatal(err)
		}
	}
	fire := func(id string, ago time.Duration, sid, outcome, line string) {
		t.Helper()
		if _, err := db.RecordTriggerFire(statedb.TriggerFireRow{
			TriggerID: id, TS: now.Add(-ago), TargetSessionID: sid, Outcome: outcome,
			Line: line, TextHash: triggers.TextHash(line),
		}); err != nil {
			t.Fatal(err)
		}
	}
	for i := 12; i >= 1; i-- {
		fire("systemd:agentdeck-overnight", time.Duration(i)*5*time.Minute, "", triggers.OutcomeDeadTarget, "tick: 0 waiting jobs")
	}
	fire("systemd:mail-poll", 50*time.Minute, fixtureInboxID, triggers.OutcomeDelivered, "3 new mails from billing")
	fire("systemd:mail-poll", 30*time.Minute, fixtureInboxID, triggers.OutcomeDelivered, "1 new mail from ops")
	fire("systemd:mail-poll", 10*time.Minute, fixtureInboxID, triggers.OutcomeDelivered, "2 new mails from leads")
	fire("systemd:"+longUnit, 45*time.Minute, fixtureConductorID, triggers.OutcomeDelivered, "issue #812 opened")
	fire("systemd:"+longUnit, 15*time.Minute, "", triggers.OutcomeFailed, "issue #813 opened")
	for i := 10; i >= 1; i-- {
		fire("watcher:github", time.Duration(i)*3*time.Hour/2, fixtureConductorID, triggers.OutcomeDelivered, "CI green on main\x1b[2J")
	}
	fire("session:worker-7", 20*time.Minute, fixtureInboxID, triggers.OutcomeDelivered, "done with the import")
}

func triggersFixtureViews(t *testing.T) []triggers.View {
	t.Helper()
	db := openTriggersFixtureDB(t)
	views, err := triggers.Load(db, triggers.NewResolver([]triggers.ProfileSessions{
		{Profile: "default", Sessions: triggersFixtureSessions()},
	}), "deckbox", triggersFixtureNow)
	if err != nil {
		t.Fatal(err)
	}
	return views
}

func newTriggersFixturePanel(t *testing.T, width, height int) *TriggersPanel {
	t.Helper()
	p := NewTriggersPanel()
	p.now = func() time.Time { return triggersFixtureNow }
	p.SetHost("deckbox")
	p.SetSize(width, height)
	p.SetViews(triggersFixtureViews(t), nil)
	return p
}

func triggerKey(s string) tea.KeyMsg {
	switch s {
	case "esc":
		return tea.KeyMsg{Type: tea.KeyEsc}
	case "enter":
		return tea.KeyMsg{Type: tea.KeyEnter}
	case "backspace":
		return tea.KeyMsg{Type: tea.KeyBackspace}
	case "alt+t":
		return tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'t'}, Alt: true}
	}
	return tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(s)}
}

func panelSources(p *TriggersPanel) []string {
	var out []string
	for _, v := range p.rows {
		out = append(out, v.Source)
	}
	return out
}

// newTriggersSeamAHome is a Seam A home with the fixture already loaded, as
// the background poll leaves it.
func newTriggersSeamAHome(t *testing.T) *Home {
	t.Helper()
	h := newSeamATestHome()
	h.applyTriggerViews(triggersFixtureViews(t))
	return h
}

func pressHome(t *testing.T, h *Home, key string) *Home {
	t.Helper()
	m, _ := h.Update(triggerKey(key))
	return m.(*Home)
}

func TestTriggersPanel_HotkeyOpensAndEscCloses(t *testing.T) {
	h := newTriggersSeamAHome(t)
	h = pressHome(t, h, "alt+t")
	if h.triggersPanel == nil || !h.triggersPanel.IsVisible() {
		t.Fatal("alt+t did not open the Triggers panel")
	}
	if !h.hasModalVisible() {
		t.Error("an open Triggers panel must count as a modal (hasModalVisible)")
	}
	if h.triggersPanel.reverseID != "" {
		t.Errorf("with no session selected the panel opened on a reverse view for %q", h.triggersPanel.reverseID)
	}
	if got := len(h.triggersPanel.rows); got != 5 {
		t.Errorf("panel lists %d triggers, want 5 (the session sender is hidden by default): %v", got, panelSources(h.triggersPanel))
	}
	// Keys go to the panel while it is open: "j" moves its cursor, not the
	// session list's.
	h = pressHome(t, h, "j")
	if h.triggersPanel.cursor != 1 {
		t.Errorf("j moved the panel cursor to %d, want 1", h.triggersPanel.cursor)
	}
	h = pressHome(t, h, "esc")
	if h.triggersPanel.IsVisible() {
		t.Fatal("esc did not close the Triggers panel")
	}
	if h.hasModalVisible() {
		t.Error("closed panel still reported as modal")
	}
	h = pressHome(t, h, "alt+t")
	h = pressHome(t, h, "alt+t")
	if h.triggersPanel.IsVisible() {
		t.Error("the hotkey pressed again did not close the panel")
	}
}

func TestTriggersPanel_HotkeyIsRegistered(t *testing.T) {
	if got := defaultHotkeyBindings[hotkeyTriggers]; got != "alt+t" {
		t.Fatalf("default triggers hotkey = %q, want alt+t", got)
	}
	found := false
	for _, a := range hotkeyActionOrder {
		found = found || a == hotkeyTriggers
	}
	if !found {
		t.Error("hotkeyTriggers missing from hotkeyActionOrder")
	}
	lookup, _ := buildHotkeyLookup(resolveHotkeys(nil))
	if got := lookup["alt+t"]; got != "alt+t" {
		t.Errorf("alt+t resolves to %q, want the triggers action", got)
	}
}

func TestTriggersPanel_Navigation(t *testing.T) {
	p := newTriggersFixturePanel(t, 120, 40)
	p.Show()
	want := []string{"com.ashesh.limit-guard", "agentdeck-github-issue-poll-for-the-backend-team", "agentdeck-overnight", "mail-poll", "github\x1b[2J"}
	if got := panelSources(p); strings.Join(got, ",") != strings.Join(want, ",") {
		t.Fatalf("rows (host, kind, name order) = %q, want %q", got, want)
	}
	for i := 0; i < 10; i++ {
		p, _ = p.Update(triggerKey("j"))
	}
	if p.cursor != 4 {
		t.Errorf("cursor after 10×j = %d, want clamped at 4", p.cursor)
	}
	p, _ = p.Update(triggerKey("k"))
	p, _ = p.Update(tea.KeyMsg{Type: tea.KeyUp})
	if sel := p.Selected(); sel == nil || sel.Source != "agentdeck-overnight" {
		t.Errorf("after k, up: selected %v, want agentdeck-overnight", sel)
	}
	for i := 0; i < 10; i++ {
		p, _ = p.Update(triggerKey("k"))
	}
	if p.cursor != 0 {
		t.Errorf("cursor after 10×k = %d, want 0", p.cursor)
	}
}

func TestTriggersPanel_EnterOpensReverseView(t *testing.T) {
	p := newTriggersFixturePanel(t, 120, 40)
	p.Show()
	// mail-poll wakes inbox-triage.
	for p.Selected() != nil && p.Selected().Source != "mail-poll" {
		p, _ = p.Update(triggerKey("j"))
	}
	p, _ = p.Update(triggerKey("enter"))
	if p.reverseID != fixtureInboxID {
		t.Fatalf("enter on mail-poll opened reverse view for %q, want %q", p.reverseID, fixtureInboxID)
	}
	if got := panelSources(p); strings.Join(got, ",") != "mail-poll" {
		t.Errorf("what wakes inbox-triage = %q, want only mail-poll (worker-7 is a session sender)", got)
	}
	if frame := stripAnsi(p.View()); !strings.Contains(frame, "What wakes inbox-triage") {
		t.Errorf("reverse view header missing:\n%s", frame)
	}
	// "a" (all kinds) brings the session sender in.
	p, _ = p.Update(triggerKey("a"))
	if got := panelSources(p); len(got) != 2 {
		t.Errorf("with all kinds, what wakes inbox-triage = %q, want mail-poll and worker-7", got)
	}
	p, _ = p.Update(triggerKey("a"))
	// esc leaves the reverse view and lands back on the row it came from.
	p, _ = p.Update(triggerKey("esc"))
	if !p.IsVisible() || p.reverseID != "" {
		t.Fatalf("esc in the reverse view: visible=%v reverse=%q, want the full list", p.IsVisible(), p.reverseID)
	}
	if sel := p.Selected(); sel == nil || sel.Source != "mail-poll" {
		t.Errorf("back in the list the cursor is on %v, want mail-poll", sel)
	}
	// Enter on a dead target has no session to show.
	p, _ = p.Update(triggerKey("k"))
	p, _ = p.Update(triggerKey("enter"))
	if p.reverseID != "" {
		t.Errorf("enter on a dead target opened a reverse view for %q", p.reverseID)
	}
	if !strings.Contains(p.status, "no session") {
		t.Errorf("status after enter on a dead target = %q", p.status)
	}
	p, _ = p.Update(triggerKey("esc"))
	if p.IsVisible() {
		t.Error("esc in the full list did not close the panel")
	}
}

func TestTriggersPanel_HotkeyOnWokenSessionOpensReverseView(t *testing.T) {
	h := newTriggersSeamAHome(t)
	conductor := session.NewInstanceWithTool("conductor-ashesh", "/tmp/c", "shell")
	conductor.ID = fixtureConductorID
	quiet := session.NewInstanceWithTool("quiet", "/tmp/q", "shell")
	h.flatItems = []session.Item{
		{Type: session.ItemTypeSession, Session: conductor},
		{Type: session.ItemTypeSession, Session: quiet},
	}
	h.cursor = 0
	h = pressHome(t, h, "alt+t")
	if h.triggersPanel.reverseID != fixtureConductorID {
		t.Fatalf("alt+t on a woken session opened reverse=%q, want %q", h.triggersPanel.reverseID, fixtureConductorID)
	}
	want := []string{"github\x1b[2J", "agentdeck-github-issue-poll-for-the-backend-team"}
	if got := panelSources(h.triggersPanel); strings.Join(got, ",") != strings.Join(want, ",") {
		t.Errorf("what wakes conductor-ashesh = %q, want %q (busiest first)", got, want)
	}
	h = pressHome(t, h, "esc")
	if !h.triggersPanel.IsVisible() || h.triggersPanel.reverseID != "" {
		t.Error("esc from a reverse view opened by hotkey should land on the full list")
	}
	h = pressHome(t, h, "esc")
	// A session nothing wakes opens the full list.
	h.cursor = 1
	h = pressHome(t, h, "alt+t")
	if h.triggersPanel.reverseID != "" {
		t.Errorf("alt+t on a session nothing wakes opened reverse=%q, want the full list", h.triggersPanel.reverseID)
	}
}

func TestTriggersPanel_ProblemsFilter(t *testing.T) {
	p := newTriggersFixturePanel(t, 120, 40)
	p.Show()
	p, _ = p.Update(triggerKey("p"))
	got := panelSources(p)
	for _, s := range got {
		if s == "mail-poll" {
			t.Errorf("problems only still lists the healthy, quiet mail-poll: %q", got)
		}
	}
	if len(got) != 4 {
		t.Errorf("problems only = %q, want dead, failing, never-fired and the noisy watcher", got)
	}
	if frame := stripAnsi(p.View()); !strings.Contains(frame, "problems only") {
		t.Errorf("header does not say the filter is on:\n%s", frame)
	}
	p, _ = p.Update(triggerKey("p"))
	if len(p.rows) != 5 {
		t.Errorf("p again: %d rows, want 5", len(p.rows))
	}
}

func TestTriggersPanel_DeadRowDetail(t *testing.T) {
	p := newTriggersFixturePanel(t, 120, 40)
	p.Show()
	for p.Selected() != nil && p.Selected().Source != "agentdeck-overnight" {
		p, _ = p.Update(triggerKey("j"))
	}
	frame := stripAnsi(p.View())
	for _, want := range []string{
		`agentdeck-overnight sends to "overnight-orchestrator": no session has this name`,
		"config ~/.config/systemd/user/agentdeck-overnight.service",
		"Triggers  deckbox · 5 · 1 dead · 1 failing",
		"NAME", "WAKES", "EVERY", "LAST", "NOISE",
		"✗", "○", "▾ deckbox",
	} {
		if !strings.Contains(frame, want) {
			t.Errorf("frame missing %q:\n%s", want, frame)
		}
	}
}

func TestTriggersPanel_SanitizesForeignStrings(t *testing.T) {
	p := newTriggersFixturePanel(t, 200, 50)
	p.Show()
	for p.Selected() != nil && !strings.HasPrefix(p.Selected().Source, "github") {
		p, _ = p.Update(triggerKey("j"))
	}
	if frame := p.View(); strings.Contains(frame, "\x1b[2J") {
		t.Fatal("a trigger name or line reached the terminal with its escape sequence")
	}
}

func TestTriggersPanel_LongNamesTruncateByCells(t *testing.T) {
	for _, width := range []int{80, 120, 200} {
		p := newTriggersFixturePanel(t, width, 40)
		p.Show()
		for i, line := range strings.Split(stripAnsi(p.View()), "\n") {
			if w := cellWidth(line); w > width {
				t.Errorf("width %d: line %d is %d cells:\n%s", width, i, w, line)
			}
		}
	}
}

func assertTriggersGolden(t *testing.T, name, rendered string) {
	t.Helper()
	got := strings.TrimRight(stripAnsi(rendered), "\n") + "\n"
	path := filepath.Join("testdata", "triggers", name+".txt")
	if os.Getenv("UPDATE_GOLDEN") == "1" {
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(got), 0o644); err != nil {
			t.Fatal(err)
		}
		return
	}
	want, err := os.ReadFile(path)
	if err != nil {
		// Print the frame so a run in a throwaway container (deck-test) can
		// still be reviewed and committed by hand.
		t.Fatalf("%v\n=== GOLDEN %s ===\n%s=== END ===", err, name, got)
	}
	if got != string(want) {
		t.Fatalf("%s frame differs\nwant:\n%s\ngot:\n%s", name, want, got)
	}
}

// TestTriggersPanel_Golden: the panel at the three standard sizes with the
// dead target selected (its detail line under the list), and the reverse
// view for conductor-ashesh.
func TestTriggersPanel_Golden(t *testing.T) {
	forceTrueColorProfile()
	for _, size := range [][2]int{{80, 24}, {120, 40}, {200, 50}} {
		name := strconv.Itoa(size[0]) + "x" + strconv.Itoa(size[1])
		t.Run(name, func(t *testing.T) {
			p := newTriggersFixturePanel(t, size[0], size[1])
			p.Show()
			p, _ = p.Update(triggerKey("j"))
			p, _ = p.Update(triggerKey("j"))
			assertTriggersGolden(t, "panel_"+name, p.View())
		})
	}
	t.Run("reverse_120x40", func(t *testing.T) {
		p := newTriggersFixturePanel(t, 120, 40)
		p.ShowReverse(fixtureConductorID, "conductor-ashesh")
		assertTriggersGolden(t, "reverse_120x40", p.View())
	})
	t.Run("problems_80x24", func(t *testing.T) {
		p := newTriggersFixturePanel(t, 80, 24)
		p.Show()
		p, _ = p.Update(triggerKey("p"))
		assertTriggersGolden(t, "problems_80x24", p.View())
	})
	t.Run("empty_80x24", func(t *testing.T) {
		p := NewTriggersPanel()
		p.SetHost("deckbox")
		p.SetSize(80, 24)
		p.SetViews(nil, nil)
		p.Show()
		assertTriggersGolden(t, "empty_80x24", p.View())
	})
}
