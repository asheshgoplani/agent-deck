package ui

import (
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/asheshgoplani/agent-deck/internal/session"
)

func switcherIDs(list []*session.Instance) []string {
	ids := make([]string, len(list))
	for i, inst := range list {
		ids[i] = inst.ID
	}
	return ids
}

// mruThree returns three live sessions whose LastAccessedAt order is a > b > c,
// passed in deliberately unsorted so Show must reorder them.
func mruThree() []*session.Instance {
	now := time.Now()
	a := &session.Instance{ID: "a", Title: "alpha", Tool: "claude", Status: session.StatusRunning, LastAccessedAt: now}
	b := &session.Instance{ID: "b", Title: "bravo", Tool: "claude", Status: session.StatusRunning, LastAccessedAt: now.Add(-time.Minute)}
	c := &session.Instance{ID: "c", Title: "charlie", Tool: "gemini", Status: session.StatusIdle, LastAccessedAt: now.Add(-time.Hour)}
	return []*session.Instance{c, b, a}
}

func TestSessionSwitcher_ShowOrdersMRUAndPreselectsCurrent(t *testing.T) {
	sw := NewSessionSwitcher()
	if !sw.Show("a", mruThree(), nil) {
		t.Fatal("expected switcher to show with 3 live sessions")
	}
	if !sw.IsVisible() {
		t.Fatal("switcher should be visible")
	}
	if got := switcherIDs(sw.sessions); got[0] != "a" || got[1] != "b" || got[2] != "c" {
		t.Fatalf("MRU order = %v, want [a b c]", got)
	}
	// The picker opens on the session we came from, so Enter returns there.
	if sel := sw.GetSelected(); sel == nil || sel.ID != "a" {
		t.Fatalf("initial selection = %v, want a (the origin session)", sel)
	}
}

func TestSessionSwitcher_FewerThanTwoActiveReturnsFalse(t *testing.T) {
	sw := NewSessionSwitcher()
	if sw.Show("a", []*session.Instance{{ID: "a", Status: session.StatusRunning}}, nil) {
		t.Fatal("Show should return false with a single session")
	}
	if sw.IsVisible() {
		t.Fatal("switcher must not be visible when it could not show")
	}
	// Two total but one stopped => only one switchable => still false.
	twoOneDead := []*session.Instance{
		{ID: "a", Status: session.StatusRunning},
		{ID: "b", Status: session.StatusStopped},
	}
	if sw.Show("a", twoOneDead, nil) {
		t.Fatal("Show should return false when only one session is live")
	}
}

func TestSessionSwitcher_FiltersStoppedAndError(t *testing.T) {
	now := time.Now()
	list := []*session.Instance{
		{ID: "a", Status: session.StatusRunning, LastAccessedAt: now},
		{ID: "b", Status: session.StatusError, LastAccessedAt: now},
		{ID: "c", Status: session.StatusStopped, LastAccessedAt: now},
		{ID: "d", Status: session.StatusWaiting, LastAccessedAt: now.Add(-time.Minute)},
	}
	sw := NewSessionSwitcher()
	if !sw.Show("a", list, nil) {
		t.Fatal("expected switcher to show (2 live sessions)")
	}
	if got := switcherIDs(sw.sessions); len(got) != 2 {
		t.Fatalf("live sessions = %v, want only a and d", got)
	}
	for _, inst := range sw.sessions {
		if inst.ID == "b" || inst.ID == "c" {
			t.Fatalf("dead session %s leaked into switcher", inst.ID)
		}
	}
}

func TestSessionSwitcher_NextPrevWrap(t *testing.T) {
	sw := NewSessionSwitcher()
	sw.Show("a", mruThree(), nil) // cursor starts at a (the origin, index 0)

	sw.next() // -> b (1)
	if sw.GetSelected().ID != "b" {
		t.Fatalf("after next, got %s, want b", sw.GetSelected().ID)
	}
	sw.next() // -> c (2)
	sw.next() // -> a (0), wrap
	if sw.GetSelected().ID != "a" {
		t.Fatalf("after forward wrap, got %s, want a", sw.GetSelected().ID)
	}
	sw.prev() // -> c (2), wrap back
	if sw.GetSelected().ID != "c" {
		t.Fatalf("after backward wrap, got %s, want c", sw.GetSelected().ID)
	}
}

func TestSessionSwitcher_CycleThrottlesKeyRepeat(t *testing.T) {
	sw := NewSessionSwitcher()
	sw.Show("a", mruThree(), nil) // cursor at a (0); lastCycleAt zero
	base := time.Unix(1000, 0)

	if !sw.cycle(true, base) { // a -> b (1)
		t.Fatal("first cycle should advance")
	}
	if sw.GetSelected().ID != "b" {
		t.Fatalf("after first cycle, got %s, want b", sw.GetSelected().ID)
	}
	// A repeat within the guard window is swallowed (no spin).
	if sw.cycle(true, base.Add(10*time.Millisecond)) {
		t.Fatal("rapid key-repeat should be throttled")
	}
	if sw.GetSelected().ID != "b" {
		t.Fatalf("throttled repeat moved cursor to %s", sw.GetSelected().ID)
	}
	// A deliberate tap after the guard window advances again.
	if !sw.cycle(true, base.Add(200*time.Millisecond)) { // b -> c (2)
		t.Fatal("tap after guard window should advance")
	}
	if sw.GetSelected().ID != "c" {
		t.Fatalf("after second tap, got %s, want c", sw.GetSelected().ID)
	}
}

func TestSessionSwitcher_ShowRecordsOrigin(t *testing.T) {
	sw := NewSessionSwitcher()
	sw.Show("b", mruThree(), nil)
	if sw.fromID != "b" {
		t.Fatalf("fromID = %q, want b (the session the picker opened from)", sw.fromID)
	}
}

func TestSessionSwitcher_HideResetsState(t *testing.T) {
	sw := NewSessionSwitcher()
	sw.Show("a", mruThree(), nil)
	sw.Hide()
	if sw.IsVisible() {
		t.Error("switcher should be hidden after Hide")
	}
	if sw.sessions != nil || sw.cursor != 0 || sw.fromID != "" {
		t.Error("Hide should reset sessions, cursor, and fromID")
	}
	if sw.GetSelected() != nil {
		t.Error("GetSelected should be nil after Hide")
	}
}

func TestSessionSwitcher_CommitGenIsMonotonic(t *testing.T) {
	sw := NewSessionSwitcher()
	g1 := sw.bumpCommitGen()
	g2 := sw.bumpCommitGen()
	if g2 <= g1 {
		t.Fatalf("bumpCommitGen should be monotonic: g1=%d g2=%d", g1, g2)
	}
	// Hide must NOT reset the generation, or a stale timer from a prior
	// switcher session could collide with a fresh one.
	sw.Hide()
	if g3 := sw.bumpCommitGen(); g3 <= g2 {
		t.Fatalf("bumpCommitGen after Hide should keep increasing: g2=%d g3=%d", g2, g3)
	}
}

func TestOpenSessionSwitcher_DoesNotArmAutoCommit(t *testing.T) {
	h := &Home{sessionSwitcher: NewSessionSwitcher()}
	h.instances = []*session.Instance{
		{ID: "a", Status: session.StatusRunning, LastAccessedAt: time.Unix(1000, 0)},
		{ID: "b", Status: session.StatusRunning, LastAccessedAt: time.Unix(900, 0)},
	}
	// A timer generation left over from a prior picker session.
	staleGen := h.sessionSwitcher.bumpCommitGen()

	// openSessionSwitcher returns nothing — it structurally cannot schedule an
	// idle-commit timer; auto-commit arms only when the user cycles in-picker.
	h.openSessionSwitcher("a", true)
	if !h.sessionSwitcher.IsVisible() {
		t.Fatal("switcher should be visible after open")
	}
	// Opening invalidates the stale timer, so a commit at the pre-open
	// generation is ignored (the freshly opened picker is not auto-committed).
	if cmd := h.handleSwitcherCommit(switcherCommitMsg{gen: staleGen}); cmd != nil {
		t.Fatal("a pre-open (stale) auto-commit timer must be ignored after re-open")
	}
}

func TestSwitcherEsc_OverviewOpenClosesWithoutReattach(t *testing.T) {
	h := &Home{sessionSwitcher: NewSessionSwitcher()}
	h.sessionSwitcher.Show("a", mruThree(), nil)
	h.sessionSwitcher.reattachOnCancel = false // opened from the overview

	_, cmd := h.handleSessionSwitcherKey(tea.KeyMsg{Type: tea.KeyEsc})
	if h.sessionSwitcher.IsVisible() {
		t.Fatal("Esc should close the switcher")
	}
	if cmd != nil {
		t.Fatal("Esc from an overview-opened switcher must not re-attach (want nil cmd)")
	}
}

func TestHandleSwitcherCommit_IgnoresStaleAndHidden(t *testing.T) {
	h := &Home{sessionSwitcher: NewSessionSwitcher()}

	// Hidden switcher: any commit is ignored.
	if cmd := h.handleSwitcherCommit(switcherCommitMsg{gen: 0}); cmd != nil {
		t.Fatal("commit while hidden should be ignored")
	}

	h.sessionSwitcher.Show("a", mruThree(), nil)
	cur := h.sessionSwitcher.bumpCommitGen()
	// A stale timer (older generation) must not commit.
	if cmd := h.handleSwitcherCommit(switcherCommitMsg{gen: cur - 1}); cmd != nil {
		t.Fatal("stale-gen commit should be ignored")
	}
	if !h.sessionSwitcher.IsVisible() {
		t.Fatal("switcher should remain visible after an ignored commit")
	}
}

func TestSessionSwitcher_ViewRendersTitlesAndFooter(t *testing.T) {
	InitTheme("dark")
	sw := NewSessionSwitcher()
	sw.SetSize(80, 24)
	subtitles := map[string]string{"b": "refactor the parser"}
	sw.Show("a", mruThree(), subtitles)

	view := sw.View()
	for _, want := range []string{"Switch session", "alpha", "bravo", "charlie", "attach"} {
		if !strings.Contains(view, want) {
			t.Errorf("view missing %q", want)
		}
	}
	// The dim conversation/pane title should appear next to its session.
	if !strings.Contains(view, "refactor the parser") {
		t.Error("view should render the session's conversation subtitle")
	}
}

// TestSessionSwitcher_ViewUsesAutoNameDescription guards the bug where the
// switcher showed an auto-named session's random handle even though the overview
// had already swapped in Claude's task description. Both render paths now route
// through sessionDisplayLabels, so the switcher must show the live pane title
// (or, when there is no live title, the persisted description) in place of the
// handle — and must NOT render the pane title a second time as a dim subtitle.
func TestSessionSwitcher_ViewUsesAutoNameDescription(t *testing.T) {
	InitTheme("dark")

	now := time.Now()
	// Auto-named quick session: Title is the machine handle; the live Claude
	// task description arrives via the subtitles map (the cleaned pane title).
	live := &session.Instance{ID: "x", Title: "amber-fox", Tool: "claude", Status: session.StatusRunning, LastAccessedAt: now}
	live.SetAutoName(true)
	// Auto-named session with no live pane title but a persisted description —
	// the switcher should fall back to it, mirroring displaySessionTitle.
	persisted := &session.Instance{ID: "y", Title: "brave-otter", Tool: "claude", Status: session.StatusRunning, LastAccessedAt: now.Add(-time.Minute)}
	persisted.SetAutoName(true)
	persisted.SetAutoNameDescription("review the migration")

	sw := NewSessionSwitcher()
	sw.SetSize(80, 24)
	sw.Show("x", []*session.Instance{live, persisted}, map[string]string{"x": "fix the login bug"})

	view := sw.View()
	if !strings.Contains(view, "fix the login bug") {
		t.Errorf("switcher should show the live Claude task description, got:\n%s", view)
	}
	if strings.Contains(view, "amber-fox") {
		t.Errorf("switcher should not show the random handle for an auto-named session, got:\n%s", view)
	}
	if !strings.Contains(view, "review the migration") {
		t.Errorf("switcher should fall back to the persisted auto-name description, got:\n%s", view)
	}
	if strings.Contains(view, "brave-otter") {
		t.Errorf("switcher should not show the handle when a persisted description exists, got:\n%s", view)
	}
	// The live title must appear exactly once (as the title), not also as a dim
	// subtitle — an auto-named row promotes the pane title to the title.
	if n := strings.Count(view, "fix the login bug"); n != 1 {
		t.Errorf("auto-named pane title should render once (as the title), got %d occurrences:\n%s", n, view)
	}
}

// maxLineCellWidth returns the widest rendered line of view in terminal cells.
func maxLineCellWidth(view string) int {
	widest := 0
	for _, line := range strings.Split(view, "\n") {
		if w := cellWidth(line); w > widest {
			widest = w
		}
	}
	return widest
}

// TestSessionSwitcher_ViewAutoExpandsToFitLongTitles pins the auto-expand
// behavior: the dialog grows past its default width to show a long title in
// full when the terminal is wide enough, and clamps to the terminal width
// (truncating) when it is not — it must never render wider than the terminal.
func TestSessionSwitcher_ViewAutoExpandsToFitLongTitles(t *testing.T) {
	InitTheme("dark")

	now := time.Now()
	longTitle := "implement the new authentication flow with oauth and refresh token rotation"
	long := &session.Instance{ID: "x", Title: longTitle, Tool: "claude", Status: session.StatusRunning, LastAccessedAt: now}
	other := &session.Instance{ID: "y", Title: "short", Tool: "claude", Status: session.StatusRunning, LastAccessedAt: now.Add(-time.Minute)}
	list := []*session.Instance{long, other}

	// Wide terminal: the box should expand to show the whole long title. The
	// title alone exceeds the default 56-wide box, so its full presence proves
	// the dialog grew.
	sw := NewSessionSwitcher()
	sw.SetSize(200, 24)
	sw.Show("x", list, nil)
	if v := sw.View(); !strings.Contains(v, longTitle) {
		t.Errorf("wide terminal should render the full long title, got:\n%s", v)
	}

	// Narrow terminal: the box must stay within the terminal width and truncate.
	const narrow = 50
	sw.SetSize(narrow, 24)
	sw.Show("x", list, nil)
	v := sw.View()
	if strings.Contains(v, longTitle) {
		t.Errorf("narrow terminal should truncate the long title, got:\n%s", v)
	}
	if !strings.Contains(v, "…") {
		t.Errorf("narrow terminal should show a truncation ellipsis, got:\n%s", v)
	}
	if w := maxLineCellWidth(v); w > narrow {
		t.Errorf("rendered switcher width %d exceeds terminal width %d:\n%s", w, narrow, v)
	}
}

// TestSessionSwitcher_FooterEscReflectsContext pins the Esc hint: it says
// "Esc back" only when the picker was opened while attached (Esc re-attaches),
// and "Esc close" when opened from the overview (Esc just closes).
func TestSessionSwitcher_FooterEscReflectsContext(t *testing.T) {
	InitTheme("dark")
	sw := NewSessionSwitcher()
	sw.SetSize(80, 24)

	sw.Show("a", mruThree(), nil) // reattachOnCancel defaults to false (overview)
	if v := sw.View(); !strings.Contains(v, "Esc close") || strings.Contains(v, "Esc back") {
		t.Errorf("overview-opened footer should say 'Esc close', got:\n%s", v)
	}

	sw.reattachOnCancel = true // opened while attached
	if v := sw.View(); !strings.Contains(v, "Esc back") {
		t.Errorf("attached-opened footer should say 'Esc back', got:\n%s", v)
	}
}

// TestSessionSwitcher_RemoteSessionsUnsupported documents a deliberate scope
// decision flagged by the Remote_parity check: the in-attach switcher operates
// on local *session.Instance rows and re-attaches via the local tmux attach
// loop only. Remote (SSH) sessions use a separate attach path, so they are
// intentionally excluded from the picker for now — mirroring them would require
// a remote re-attach path that is out of scope for this feature. Tracked as a
// follow-up. See SessionSwitcher.Show / Home.openSessionSwitcher.
func TestSessionSwitcher_RemoteSessionsUnsupported(t *testing.T) {
	t.Skip("by design: the session switcher is local-only; remote (SSH) sessions use a separate attach path — follow-up tracked")
}

// TestCtrlS_NewDialogOpen_DoesNotOpenSwitcher guards the binding collision the
// maintainer flagged: as of v1.9.57 the new-session dialog uses Ctrl+S as its
// submit key, so the overview Ctrl+S switcher must never fire while that dialog
// is open. The protection is the modal-dispatch order in Update (newDialog is
// checked before handleMainKey, where the overview Ctrl+S lives); this pins
// that contract end-to-end through Update.
func TestCtrlS_NewDialogOpen_DoesNotOpenSwitcher(t *testing.T) {
	h := &Home{
		setupWizard:     NewSetupWizard(),
		watcherPanel:    NewWatcherPanel(),
		settingsPanel:   NewSettingsPanel(),
		helpOverlay:     NewHelpOverlay(),
		search:          NewSearch(),
		globalSearch:    NewGlobalSearch(),
		newDialog:       NewNewDialog(),
		sessionSwitcher: NewSessionSwitcher(),
	}
	// Two live sessions, so the switcher *could* open if routing were wrong.
	h.instances = []*session.Instance{
		{ID: "a", Status: session.StatusRunning, LastAccessedAt: time.Unix(1000, 0)},
		{ID: "b", Status: session.StatusRunning, LastAccessedAt: time.Unix(900, 0)},
	}
	h.instanceByID = map[string]*session.Instance{"a": h.instances[0], "b": h.instances[1]}
	h.newDialog.Show() // the new-session dialog is now the active modal

	if _, _ = h.Update(tea.KeyMsg{Type: tea.KeyCtrlS}); h.sessionSwitcher.IsVisible() {
		t.Fatal("Ctrl+S while the new-session dialog is open must NOT open the session switcher (dialog submit takes precedence)")
	}
	// The dialog stays open: an empty-name submit is a validation no-op, proving
	// Ctrl+S was routed to the dialog rather than the switcher.
	if !h.newDialog.IsVisible() {
		t.Fatal("the new-session dialog should remain open after an empty-name Ctrl+S submit")
	}
}
