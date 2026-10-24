package ui

import (
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/x/ansi"

	"github.com/asheshgoplani/agent-deck/internal/session"
	"github.com/asheshgoplani/agent-deck/internal/sessionref"
	"github.com/asheshgoplani/agent-deck/internal/tmux"
)

// refInsertCall is one call of the fake insert: what the real sessionref.Hand
// would have typed into B's pane.
type refInsertCall struct {
	target *session.Instance
	line   string
}

type refHandCall struct {
	src, dst sessionref.Endpoint
	opts     sessionref.Options
}

// refFixture is a Home with one group holding alpha (A), beta (B) and a
// remote-host session, then a second group header, and fakes in place of tmux.
type refFixture struct {
	h       *Home
	a, b    *session.Instance
	ssh     *session.Instance
	inserts []refInsertCall
	hands   []refHandCall
	running bool
	// pane is what the core's menu guard reads from B (an idle composer
	// unless a test opens a menu).
	pane     string
	captures int
}

const (
	refRowGroup  = 0
	refRowA      = 1
	refRowB      = 2
	refRowSSH    = 3
	refRowGroup2 = 4
)

func newRefFixture(t *testing.T) *refFixture {
	t.Helper()
	f := &refFixture{running: true, pane: "❯ \n"}
	f.a = &session.Instance{ID: "aaaa1111-1700000000", Title: "alpha", ProjectPath: "/work/alpha", Tool: "claude", Status: session.StatusRunning}
	f.b = &session.Instance{ID: "bbbb2222-1700000000", Title: "beta", ProjectPath: "/work/beta", Tool: "claude", Status: session.StatusWaiting}
	f.ssh = &session.Instance{ID: "cccc3333-1700000000", Title: "far", ProjectPath: "/srv/far", Tool: "claude", Status: session.StatusIdle, SSHHost: "build-box"}
	items := []session.Item{
		{Type: session.ItemTypeGroup, Path: "work", Group: &session.Group{Name: "work", Path: "work"}},
		{Type: session.ItemTypeSession, Session: f.a, Path: "work", Level: 1},
		{Type: session.ItemTypeSession, Session: f.b, Path: "work", Level: 1},
		{Type: session.ItemTypeSession, Session: f.ssh, Path: "work", Level: 1},
		{Type: session.ItemTypeGroup, Path: "infra", Group: &session.Group{Name: "infra", Path: "infra"}},
	}
	h := newTestHomeWithItems(120, 30, items)
	h.instances = []*session.Instance{f.a, f.b, f.ssh}
	h.instanceByID = map[string]*session.Instance{f.a.ID: f.a, f.b.ID: f.b, f.ssh.ID: f.ssh}
	h.profile = "work"
	h.sessionExists = func(*session.Instance) bool { return f.running }
	h.sessionRef.insert = func(target *session.Instance, line string) error {
		f.inserts = append(f.inserts, refInsertCall{target: target, line: line})
		return nil
	}
	h.sessionRef.capture = func(*session.Instance) (string, error) {
		f.captures++
		return f.pane, nil
	}
	h.sessionRef.hand = func(src, dst sessionref.Endpoint, opts sessionref.Options) (*sessionref.Result, error) {
		f.hands = append(f.hands, refHandCall{src: src, dst: dst, opts: opts})
		return sessionref.Hand(src, dst, opts) // the real core, fake insert
	}
	h.cursor = refRowA
	f.h = h
	return f
}

var (
	keyAltL  = tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'l'}, Alt: true}
	keyEnter = tea.KeyMsg{Type: tea.KeyEnter}
	keyEsc   = tea.KeyMsg{Type: tea.KeyEsc}
	keyDown  = tea.KeyMsg{Type: tea.KeyDown}
	keyUp    = tea.KeyMsg{Type: tea.KeyUp}
)

func runeKey(r rune) tea.KeyMsg { return tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{r}} }

// press sends a key through the real main-key path and returns the command.
func (f *refFixture) press(t *testing.T, msg tea.KeyMsg) tea.Cmd {
	t.Helper()
	_, cmd := f.h.handleMainKey(msg)
	return cmd
}

// finish runs a drop command the way Bubble Tea would and feeds its message
// back into Update.
func (f *refFixture) finish(t *testing.T, cmd tea.Cmd) {
	t.Helper()
	if cmd == nil {
		t.Fatal("drop returned no command")
	}
	msg := cmd()
	res, ok := msg.(sessionRefResultMsg)
	if !ok {
		t.Fatalf("drop command produced %T, want sessionRefResultMsg", msg)
	}
	f.h.Update(res)
}

func (f *refFixture) hint() string { return f.h.sessionRefHintText() }

func TestSessionRefHotkeyDefaultIsAltLAndFree(t *testing.T) {
	bindings := resolveHotkeys(nil)
	if got := bindings[hotkeySessionRef]; got != "alt+l" {
		t.Fatalf("default session_ref binding = %q, want alt+l", got)
	}
	seen := map[string]string{}
	for action, key := range bindings {
		if other, dup := seen[key]; dup {
			t.Errorf("actions %q and %q both bind %q", other, action, key)
		}
		seen[key] = action
	}
	// The parallel feedback lane (ASK-109) takes "Z"; this action must not.
	if bindings[hotkeySessionRef] == "Z" {
		t.Error("session_ref must not take Z (report_problem)")
	}

	h := &Home{}
	h.setHotkeys(resolveHotkeys(map[string]string{"session_ref": "ctrl+g"}))
	if got := h.normalizeMainKey("ctrl+g"); got != "alt+l" {
		t.Errorf("rebound ctrl+g normalized to %q, want alt+l", got)
	}
	if got := h.normalizeMainKey("alt+l"); got != "" {
		t.Errorf("vacated alt+l normalized to %q, want blocked", got)
	}
	h.setHotkeys(resolveHotkeys(map[string]string{"session_ref": ""}))
	if got := h.normalizeMainKey("alt+l"); got != "" {
		t.Errorf("unbound session_ref: alt+l normalized to %q, want blocked", got)
	}
}

func TestSessionRefPickShowsArrowHintFollowingCursor(t *testing.T) {
	f := newRefFixture(t)
	f.press(t, keyAltL)
	if !f.h.sessionRefPicking() {
		t.Fatal("alt+l on a session did not start picking")
	}
	if got, want := f.hint(), "◆ alpha -> ✗ that is the same session: pick a different one"; got != want {
		t.Errorf("hint on A itself = %q, want %q", got, want)
	}
	f.press(t, keyDown)
	if f.h.cursor != refRowB {
		t.Fatalf("down while picking moved cursor to %d, want %d", f.h.cursor, refRowB)
	}
	if got, want := f.hint(), "◆ alpha ───▶ beta   ⏎ drop · esc cancel · nothing is sent"; got != want {
		t.Errorf("hint on B = %q, want %q", got, want)
	}
	f.press(t, runeKey('j'))
	if got := f.hint(); got != "◆ alpha -> ✗ remote sessions can't take a reference yet" {
		t.Errorf("hint on remote-host row = %q", got)
	}
	f.press(t, runeKey('j'))
	if got := f.hint(); got != "◆ alpha -> ✗ infra is a group: drop onto a session, not a group (no move)" {
		t.Errorf("hint on group row = %q", got)
	}
	if len(f.inserts) != 0 || len(f.hands) != 0 {
		t.Fatalf("moving while picking reached the core: hands=%d inserts=%d", len(f.hands), len(f.inserts))
	}
}

func TestSessionRefPickBarReplacesHelpBarAndMarksSource(t *testing.T) {
	f := newRefFixture(t)
	// The normal footer is saturated (an extra hint pushed Undo off at 200
	// columns), so it stays as it was; the help screen and the picking bar
	// carry the key.
	if bar := ansi.Strip(f.h.renderHelpBar()); strings.Contains(bar, "Drop on this session") {
		t.Errorf("normal help bar shows picking keys before a pick:\n%s", bar)
	}
	f.press(t, keyAltL)
	f.press(t, keyDown)
	view := ansi.Strip(f.h.View())
	lines := strings.Split(view, "\n")
	if len(lines) != f.h.height {
		t.Errorf("frame height while picking = %d, want %d (pick bar must keep 2 lines)", len(lines), f.h.height)
	}
	last2 := strings.Join(lines[len(lines)-2:], "\n")
	if !strings.Contains(last2, "◆ alpha ───▶ beta") {
		t.Errorf("bottom of frame lacks the arrow hint:\n%s", last2)
	}
	if !strings.Contains(last2, "Drop on this session │ esc Cancel") {
		t.Errorf("bottom of frame lacks the picking keys:\n%s", last2)
	}
	marked := false
	for _, l := range lines {
		if strings.HasPrefix(l, "◆") && strings.Contains(l, "alpha") {
			marked = true
		}
	}
	if !marked {
		t.Errorf("A's row is not marked with ◆ while picking:\n%s", view)
	}
	f.press(t, keyEsc)
	if after := ansi.Strip(f.h.View()); strings.Contains(after, "───▶") {
		t.Errorf("hint line still drawn after Esc:\n%s", after)
	}
}

func TestSessionRefEnterDropsIntoBWithoutSubmitting(t *testing.T) {
	f := newRefFixture(t)
	f.press(t, keyAltL)
	f.press(t, keyDown)
	cmd := f.press(t, keyEnter)
	if f.h.sessionRefPicking() {
		t.Error("picking still live after an accepted drop")
	}
	f.finish(t, cmd)

	if len(f.hands) != 1 {
		t.Fatalf("core called %d times, want 1", len(f.hands))
	}
	got := f.hands[0]
	if got.src.Inst != f.a || got.dst.Inst != f.b {
		t.Errorf("core called with src=%v dst=%v, want alpha -> beta", got.src.Inst.Title, got.dst.Inst.Title)
	}
	if got.src.Profile != "work" || got.dst.Profile != "work" {
		t.Errorf("core called with profiles %q/%q, want the deck's profile", got.src.Profile, got.dst.Profile)
	}
	if got.opts.PrintOnly {
		t.Error("TUI drop must insert, not print")
	}
	if len(f.inserts) != 1 {
		t.Fatalf("insert called %d times, want exactly 1", len(f.inserts))
	}
	ins := f.inserts[0]
	if ins.target != f.b {
		t.Errorf("inserted into %q, want beta", ins.target.Title)
	}
	if strings.ContainsAny(ins.line, "\r\n") {
		t.Errorf("inserted line carries a line break (would submit): %q", ins.line)
	}
	for _, want := range []string{`"alpha"`, f.a.ID, "agent-deck -p work session output " + f.a.ID + " -q"} {
		if !strings.Contains(ins.line, want) {
			t.Errorf("inserted line %q lacks %q", ins.line, want)
		}
	}
	if !f.h.sessionRefToastActive() || !f.h.sessionRef.toastOK {
		t.Fatalf("no success toast after drop (err=%v)", f.h.err)
	}
	if msg := f.h.err.Error(); !strings.Contains(msg, "not sent") || !strings.Contains(msg, "beta") {
		t.Errorf("toast = %q, want it to say it went to beta and was not sent", msg)
	}
	view := ansi.Strip(f.h.View())
	if !strings.Contains(view, "✓ reference to alpha typed into beta (not sent)") {
		t.Errorf("success toast not drawn over the help bar:\n%s", view)
	}
}

func TestSessionRefAltLAgainDrops(t *testing.T) {
	f := newRefFixture(t)
	f.press(t, keyAltL)
	f.press(t, keyDown)
	f.finish(t, f.press(t, keyAltL))
	if len(f.inserts) != 1 || f.inserts[0].target != f.b {
		t.Fatalf("alt+l on B did not drop into beta: %+v", f.inserts)
	}
}

func TestSessionRefEscCancelsWithoutQuitOrInsert(t *testing.T) {
	f := newRefFixture(t)
	f.press(t, keyAltL)
	f.press(t, keyDown)
	if cmd := f.press(t, keyEsc); cmd != nil {
		t.Errorf("Esc while picking returned a command (%T)", cmd())
	}
	if f.h.sessionRefPicking() {
		t.Fatal("Esc did not cancel picking")
	}
	if !f.h.lastEscTime.IsZero() {
		t.Error("Esc while picking armed the double-Esc quit timer")
	}
	// A second Esc right after is an ordinary Esc again, not a quit.
	if f.h.isQuitting {
		t.Error("Esc while picking started quitting")
	}
	// Enter after cancelling is the ordinary attach path, not a drop.
	if len(f.hands) != 0 || len(f.inserts) != 0 {
		t.Fatalf("cancelled pick reached the core: hands=%d inserts=%d", len(f.hands), len(f.inserts))
	}
}

func TestSessionRefGroupRowIsNotATargetAndNothingMoves(t *testing.T) {
	f := newRefFixture(t)
	f.press(t, keyAltL)
	f.h.cursor = refRowGroup2
	nItems := len(f.h.flatItems)
	if cmd := f.press(t, keyEnter); cmd != nil {
		t.Error("Enter on a group while picking returned a command")
	}
	if !f.h.sessionRefPicking() {
		t.Error("refused group drop ended picking; it must stay live")
	}
	if len(f.hands) != 0 || len(f.inserts) != 0 {
		t.Fatalf("group drop reached the core: hands=%d inserts=%d", len(f.hands), len(f.inserts))
	}
	if f.h.err == nil || !strings.Contains(f.h.err.Error(), "not a group") {
		t.Errorf("group drop toast = %v, want a 'not a group' refusal", f.h.err)
	}
	if len(f.h.flatItems) != nItems || f.h.cursor != refRowGroup2 {
		t.Error("Enter on a group while picking toggled or moved the list")
	}
	// Moving on clears the refusal so it never sits under another row's hint.
	f.press(t, keyUp)
	if f.h.err != nil {
		t.Errorf("refusal toast survived moving to another row: %v", f.h.err)
	}
	f.press(t, keyDown)
	// M (move to group) does nothing while picking: no move dialog.
	f.press(t, runeKey('M'))
	if f.h.groupDialog.IsVisible() {
		t.Error("M while picking opened the move-to-group dialog")
	}
	if f.a.GroupPath != "" || f.b.GroupPath != "" {
		t.Error("a session changed group during a pick")
	}
}

func TestSessionRefMoveToGroupKeyUnchangedOutsidePicking(t *testing.T) {
	f := newRefFixture(t)
	f.h.cursor = refRowB
	f.press(t, runeKey('M'))
	if !f.h.groupDialog.IsVisible() {
		t.Fatal("M on a session no longer opens the move-to-group dialog")
	}
	if f.h.sessionRefPicking() {
		t.Error("M started a reference pick")
	}
}

func TestSessionRefSameAndRemoteRefusedInPlace(t *testing.T) {
	f := newRefFixture(t)
	f.press(t, keyAltL)
	f.press(t, keyEnter) // cursor still on A
	if !f.h.sessionRefPicking() || len(f.hands) != 0 {
		t.Fatalf("drop on A itself: picking=%v hands=%d", f.h.sessionRefPicking(), len(f.hands))
	}
	if !strings.Contains(f.h.err.Error(), "same session") {
		t.Errorf("same-session toast = %q", f.h.err)
	}
	f.h.cursor = refRowSSH
	f.press(t, keyEnter)
	if !f.h.sessionRefPicking() || len(f.hands) != 0 {
		t.Fatalf("drop on remote: picking=%v hands=%d", f.h.sessionRefPicking(), len(f.hands))
	}
	if !strings.Contains(f.h.err.Error(), "remote sessions can't take a reference") {
		t.Errorf("remote toast = %q", f.h.err)
	}
	view := ansi.Strip(f.h.View())
	if !strings.Contains(view, "✗ remote sessions can't take a reference yet") {
		t.Errorf("refusal toast not visible:\n%s", view)
	}
}

func TestSessionRefRemoteRowAndGroupCannotBePicked(t *testing.T) {
	f := newRefFixture(t)
	f.h.cursor = refRowSSH
	f.press(t, keyAltL)
	if f.h.sessionRefPicking() {
		t.Error("a remote-host session was picked as a source")
	}
	f.h.flatItems = append(f.h.flatItems, session.Item{Type: session.ItemTypeRemoteSession, RemoteName: "box",
		RemoteSession: &session.RemoteSessionInfo{ID: "r1", Title: "remote-one"}})
	f.h.cursor = len(f.h.flatItems) - 1
	f.press(t, keyAltL)
	if f.h.sessionRefPicking() {
		t.Error("a remote row was picked as a source")
	}
	f.h.cursor = refRowGroup
	f.press(t, keyAltL)
	if f.h.sessionRefPicking() {
		t.Error("a group row was picked as a source")
	}
	if f.h.err == nil || !strings.Contains(f.h.err.Error(), "not a group") {
		t.Errorf("group pick toast = %v", f.h.err)
	}
}

func TestSessionRefCoreRefusalKeepsPick(t *testing.T) {
	f := newRefFixture(t)
	f.running = false // B's pane is gone: the core refuses target_not_running
	f.press(t, keyAltL)
	f.press(t, keyDown)
	f.finish(t, f.press(t, keyEnter))
	if len(f.hands) != 1 {
		t.Fatalf("core called %d times, want 1", len(f.hands))
	}
	if len(f.inserts) != 0 {
		t.Fatalf("insert ran although B is not running: %+v", f.inserts)
	}
	if !f.h.sessionRefPicking() || f.h.sessionRef.srcID != f.a.ID {
		t.Error("a core refusal did not put the pick back")
	}
	if !strings.Contains(f.h.err.Error(), "beta is not running") || f.h.sessionRef.toastOK {
		t.Errorf("refusal toast = %q ok=%v", f.h.err, f.h.sessionRef.toastOK)
	}
}

// TestSessionRefTargetShowingMenuToastsAndKeepsPick: B shows a permission
// menu, so the real core refuses (target_menu_open) before typing; the TUI
// toasts it and keeps the pick so the user can answer B and drop again.
func TestSessionRefTargetShowingMenuToastsAndKeepsPick(t *testing.T) {
	f := newRefFixture(t)
	f.pane = " Do you want to proceed?\n ❯ 1. Yes\n   2. No, and tell Claude what to do differently (esc)\n"
	f.press(t, keyAltL)
	f.press(t, keyDown)
	f.finish(t, f.press(t, keyEnter))
	if len(f.hands) != 1 || f.captures == 0 {
		t.Fatalf("core calls=%d pane reads=%d, want the core to read B's pane", len(f.hands), f.captures)
	}
	if len(f.inserts) != 0 {
		t.Fatalf("typed into a pane showing a menu: %+v", f.inserts)
	}
	if !f.h.sessionRefPicking() || f.h.sessionRef.srcID != f.a.ID {
		t.Error("a menu refusal did not keep the pick")
	}
	if f.h.err == nil || f.h.sessionRef.toastOK || !strings.Contains(f.h.err.Error(), "beta is showing a menu/prompt; answer it first") {
		t.Fatalf("toast = %v ok=%v", f.h.err, f.h.sessionRef.toastOK)
	}
	if view := ansi.Strip(f.h.View()); !strings.Contains(view, "beta is showing a menu/prompt") {
		t.Errorf("menu refusal toast not drawn:\n%s", view)
	}

	// The user answers B; the next drop goes through.
	f.pane = "❯ \n"
	f.finish(t, f.press(t, keyEnter))
	if len(f.inserts) != 1 || f.inserts[0].target != f.b {
		t.Fatalf("after the menu closed the drop typed %+v, want one insert into beta", f.inserts)
	}
}

// TestSessionRefVimTargetWorkingToastsAndKeepsPick (R2-3): B has a vim-mode
// composer and is working, so the core refuses (target_busy_vim) before the
// Escape that would interrupt its turn; the TUI toasts it and keeps the pick,
// and the drop goes through once B is waiting.
func TestSessionRefVimTargetWorkingToastsAndKeepsPick(t *testing.T) {
	f := newRefFixture(t)
	f.b.SetTmuxSessionForTest(&tmux.Session{Name: "agentdeck_ref_vim_fake", VimMode: true})
	f.b.Status = session.StatusRunning
	f.pane = "✻ Pondering… (12s · ↑ 1.2k tokens · esc to interrupt)\n\n❯ \n"
	f.press(t, keyAltL)
	f.press(t, keyDown)
	f.finish(t, f.press(t, keyEnter))
	if len(f.hands) != 1 || f.captures == 0 {
		t.Fatalf("core calls=%d pane reads=%d, want the core to read B's pane", len(f.hands), f.captures)
	}
	if len(f.inserts) != 0 {
		t.Fatalf("typed into a working vim-mode target: %+v", f.inserts)
	}
	if !f.h.sessionRefPicking() || f.h.sessionRef.srcID != f.a.ID {
		t.Error("a busy refusal did not keep the pick")
	}
	if f.h.err == nil || f.h.sessionRef.toastOK || !strings.Contains(f.h.err.Error(), "beta is working; drop again when it is waiting") {
		t.Fatalf("toast = %v ok=%v", f.h.err, f.h.sessionRef.toastOK)
	}
	if view := ansi.Strip(f.h.View()); !strings.Contains(view, "beta is working; drop again") {
		t.Errorf("busy refusal toast not drawn:\n%s", view)
	}

	// B finishes its turn; the next drop goes through.
	f.b.Status = session.StatusWaiting
	f.pane = "❯ \n"
	f.finish(t, f.press(t, keyEnter))
	if len(f.inserts) != 1 || f.inserts[0].target != f.b {
		t.Fatalf("after B went waiting the drop typed %+v, want one insert into beta", f.inserts)
	}
}

func TestSessionRefStoppedTargetRefusedWithoutTmux(t *testing.T) {
	f := newRefFixture(t)
	f.b.Status = session.StatusStopped
	f.h.sessionExists = func(*session.Instance) bool {
		t.Fatal("the pick hint/drop probed tmux for a stopped row")
		return false
	}
	f.press(t, keyAltL)
	f.press(t, keyDown)
	if got := f.hint(); !strings.Contains(got, "✗ beta is not running: start it first") {
		t.Errorf("hint on stopped B = %q", got)
	}
	if cmd := f.press(t, keyEnter); cmd != nil || len(f.hands) != 0 {
		t.Fatal("drop on a stopped session reached the core")
	}
}

func TestSessionRefOtherActionKeysIgnoredWhilePicking(t *testing.T) {
	f := newRefFixture(t)
	f.press(t, keyAltL)
	f.press(t, keyDown)
	for _, k := range []tea.KeyMsg{runeKey('d'), runeKey('r'), runeKey('x'), runeKey('n'), runeKey('?'), runeKey('q')} {
		if cmd := f.press(t, k); cmd != nil {
			t.Errorf("%q while picking returned a command", k.String())
		}
	}
	if !f.h.sessionRefPicking() || f.h.cursor != refRowB {
		t.Error("an ignored key changed the pick or the cursor")
	}
	if f.h.helpOverlay.IsVisible() || f.h.isQuitting || f.h.sessionPickerDialog.IsVisible() || f.h.groupDialog.IsVisible() {
		t.Error("an action key opened a dialog or quit while picking")
	}
	f.press(t, keyUp)
	if f.h.cursor != refRowA {
		t.Errorf("up while picking: cursor %d, want %d", f.h.cursor, refRowA)
	}
}

func TestSessionRefHelpScreenListsKey(t *testing.T) {
	f := newRefFixture(t)
	f.h.helpOverlay.SetSize(160, 200)
	f.h.helpOverlay.Show()
	view := ansi.Strip(f.h.helpOverlay.View())
	if !strings.Contains(view, "Hand a reference to another session") {
		t.Errorf("help screen lacks the session_ref row:\n%s", view)
	}
}
