package ui

import (
	"bytes"
	"fmt"
	"io"
	"strings"
	"testing"
	"time"

	"github.com/asheshgoplani/agent-deck/internal/session"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/x/ansi"
)

func TestAltEnterSurvivesExtendedKeyboardTranslation(t *testing.T) {
	for name, input := range map[string][]byte{
		"kitty CSI u":     []byte("\x1b[13;3u"),
		"modifyOtherKeys": []byte("\x1b[27;3;13~"),
	} {
		t.Run(name, func(t *testing.T) {
			var msg *tea.KeyMsg
			if name == "kitty CSI u" {
				msg = ParseCSIu(input)
			} else {
				msg = ParseModifyOtherKeys(input)
			}
			if msg == nil || msg.String() != "alt+enter" {
				t.Fatalf("translated key = %#v, want alt+enter", msg)
			}
			out, err := io.ReadAll(NewCSIuReader(bytes.NewReader(input)))
			if err != nil {
				t.Fatalf("read translated input: %v", err)
			}
			if string(out) != "\x1b\r" {
				t.Fatalf("legacy bytes = %q, want ESC+CR", out)
			}
		})
	}
}

func armEmbeddedSessionWithFakeSender(t *testing.T) (*Home, *fakeInsertKeySender) {
	t.Helper()
	home, _, _ := armHomeWithOneSession(t)
	home.embeddedLayout = true
	home.insertKeySink = nil
	home.insertNamedKeySink = nil
	home.sessionExists = func(*session.Instance) bool { return true }

	fake := &fakeInsertKeySender{}
	home.insertOpenKeySender = func(insertTargetRef) (insertKeySender, error) {
		return fake, nil
	}

	model, _ := home.handleMainKey(tea.KeyMsg{Type: tea.KeyEnter})
	home = model.(*Home)
	return home, fake
}

func optInEmbeddedSessionSwitcher(t *testing.T) {
	t.Helper()
	setIsolatedAgentDeckDir(t)
	if err := session.SaveUserConfig(&session.UserConfig{
		Hotkeys: map[string]string{"switch_session": "ctrl+s"},
	}); err != nil {
		t.Fatalf("enable embedded session switcher: %v", err)
	}
	session.ClearUserConfigCache()
}

func TestEnterOpensEmbeddedSessionWithoutSuspendingDashboard(t *testing.T) {
	home, fake := armEmbeddedSessionWithFakeSender(t)

	if !home.embeddedMode {
		t.Fatal("Enter should activate the embedded session pane")
	}
	if !home.insertMode {
		t.Fatal("embedded session should route input through the persistent sender")
	}
	if home.isAttaching.Load() {
		t.Fatal("embedded session must not suspend Bubble Tea with full-screen attach")
	}
	if home.insertKeySender != fake {
		t.Fatal("embedded session did not retain the opened persistent sender")
	}
	view := home.View()
	if view == "" {
		t.Fatal("dashboard view must remain rendered while a session is embedded")
	}
}

func TestDetachedEmbeddedPreviewUsesFullFidelityTerminalRenderer(t *testing.T) {
	home, inst, _ := armHomeWithOneSession(t)
	home.embeddedLayout = true
	inst.Status = session.StatusRunning
	home.previewCache[inst.ID] = "\x1b[38;2;1;2;3mabc\x1b[2DXY\x1b[0m\nsecond line"

	rendered := home.renderPreviewPane(40, 6)
	plain := ansi.Strip(rendered)
	if !strings.Contains(plain, "aXY") {
		t.Fatalf("detached embedded preview did not interpret terminal cursor movement through Charm VT:\n%s", rendered)
	}
	if strings.Contains(plain, "abcXY") || strings.Contains(rendered, "\x1b[2D") {
		t.Fatalf("detached embedded preview passed terminal control sequences through as text:\n%q", rendered)
	}
	if !strings.Contains(rendered, "38;2;1;2;3") {
		t.Fatalf("detached embedded preview lost truecolor cell styling:\n%q", rendered)
	}
	for _, legacyChrome := range []string{"focused-session", "Output", "📁"} {
		if strings.Contains(plain, legacyChrome) {
			t.Fatalf("detached embedded preview still includes legacy preview chrome %q:\n%s", legacyChrome, plain)
		}
	}
	if strings.Contains(plain, "Ctrl+Q detach") {
		t.Fatalf("detached preview advertises controls that only apply after focusing the session:\n%s", plain)
	}
}

func TestDetachedClassicPreviewKeepsSessionMetadata(t *testing.T) {
	home, inst, _ := armHomeWithOneSession(t)
	inst.Status = session.StatusRunning
	home.embeddedLayout = false
	home.previewCache[inst.ID] = "terminal output"

	plain := ansi.Strip(home.renderPreviewPane(80, 20))
	for _, metadata := range []string{"focused-session", "Output", "terminal output"} {
		if !strings.Contains(plain, metadata) {
			t.Fatalf("classic detached preview lost %q:\n%s", metadata, plain)
		}
	}
}

func TestDetachedEmbeddedWindowUsesSelectedWindowSnapshot(t *testing.T) {
	home, inst, _ := armHomeWithOneSession(t)
	home.embeddedLayout = true
	inst.Status = session.StatusRunning
	home.flatItems = []session.Item{{
		Type:            session.ItemTypeWindow,
		WindowSessionID: inst.ID,
		WindowIndex:     3,
		WindowName:      "review",
	}}
	home.cursor = 0
	home.previewCache[previewCacheKey(inst.ID, 3)] = "window-abc\x1b[2DXY"

	plain := ansi.Strip(home.renderPreviewPane(40, 6))
	if !strings.Contains(plain, "window-aXY") || strings.Contains(plain, "window-abcXY") {
		t.Fatalf("detached window preview did not use the selected window's terminal snapshot:\n%s", plain)
	}
}

func TestDetachedEmbeddedRemoteUsesRemoteTerminalSnapshot(t *testing.T) {
	home := armHomeWithOneRemoteSession(t)
	home.embeddedLayout = true
	remote := home.flatItems[0].RemoteSession
	remote.Tool = "claude"
	remote.Status = string(session.StatusRunning)
	home.previewCache[remotePreviewCacheKey("lab", remote.ID)] = "remote-abc\x1b[2DXY"

	plain := ansi.Strip(home.renderPreviewPane(40, 6))
	if !strings.Contains(plain, "remote-aXY") || strings.Contains(plain, "remote-abcXY") {
		t.Fatalf("detached remote preview did not use the remote terminal snapshot:\n%s", plain)
	}
	if strings.Contains(plain, remote.Title) {
		t.Fatalf("detached remote preview still includes legacy metadata chrome:\n%s", plain)
	}
}

func TestClassicStyleEnterUsesFullScreenAttach(t *testing.T) {
	home, _, _ := armHomeWithOneSession(t)
	home.embeddedLayout = false
	home.sessionExists = func(*session.Instance) bool { return true }

	model, cmd := home.handleMainKey(tea.KeyMsg{Type: tea.KeyEnter})
	home = model.(*Home)
	defer home.isAttaching.Store(false)

	if cmd == nil {
		t.Fatal("classic Enter did not build a full-screen attach command")
	}
	if home.embeddedMode || home.insertMode {
		t.Fatal("classic Enter unexpectedly entered the embedded pane")
	}
	if !home.isAttaching.Load() {
		t.Fatal("classic Enter did not enter the full-screen attach transition")
	}
}

func TestEnterProductionPathBuildsPTYAttachWithoutKeySender(t *testing.T) {
	home, inst, _ := armHomeWithOneSession(t)
	home.embeddedLayout = true
	home.sessionExists = func(*session.Instance) bool { return true }
	home.sessionInput = &SessionInputRouter{}
	keySenderOpened := false
	home.insertOpenKeySender = func(insertTargetRef) (insertKeySender, error) {
		keySenderOpened = true
		return &fakeInsertKeySender{}, nil
	}

	model, cmd := home.handleMainKey(tea.KeyMsg{Type: tea.KeyEnter})
	home = model.(*Home)
	if !home.embeddedMode || !home.insertMode {
		t.Fatal("production Enter did not arm embedded session mode")
	}
	if cmd == nil {
		t.Fatal("production Enter did not schedule the PTY attach")
	}
	if keySenderOpened || home.insertKeySender != nil {
		t.Fatal("production embedded path must not open the lossy tmux KeySender")
	}
	tmuxSession := inst.GetTmuxSession()
	if tmuxSession == nil {
		t.Fatal("test session has no tmux target")
	}
	if !home.embeddedRequest.ForceUTF8 {
		t.Fatal("embedded attach request must force UTF-8 like the full-screen attach path")
	}
	if got := home.embeddedRequest.Name; got != tmuxSession.Name {
		t.Fatalf("attach request target = %q, want %q", got, tmuxSession.Name)
	}
}

func TestEmbeddedCtrlQRefreshesDetachedTerminalSnapshot(t *testing.T) {
	home, _ := armEmbeddedSessionWithFakeSender(t)

	model, cmd := home.handleInsertModeKey(tea.KeyMsg{Type: tea.KeyCtrlQ})
	home = model.(*Home)
	if home.embeddedMode || home.insertMode {
		t.Fatal("Ctrl+Q did not detach the embedded session")
	}
	if cmd == nil {
		t.Fatal("Ctrl+Q did not schedule a fresh detached terminal snapshot")
	}
}

func TestEmbeddedCtrlSOpensSessionSwitcher(t *testing.T) {
	optInEmbeddedSessionSwitcher(t)
	home := NewHome()
	home.embeddedLayout = true
	home.width = 120
	home.height = 40
	home.instances = mruThree()
	home.sessionInput = &SessionInputRouter{active: true, switchByte: 0x13}
	home.embeddedMode = true
	home.insertMode = true
	home.insertModeSessionID = "a"

	model, cmd := home.handleInsertModeKey(tea.KeyMsg{Type: tea.KeyCtrlS})
	home = model.(*Home)
	if cmd != nil {
		t.Fatal("opening the embedded session switcher should not schedule a command")
	}
	if home.embeddedMode || home.insertMode {
		t.Fatal("Ctrl+S did not leave the embedded session before opening the switcher")
	}
	if !home.sessionSwitcher.IsVisible() {
		t.Fatal("Ctrl+S did not open the session switcher")
	}
	if got := home.sessionSwitcher.fromID; got != "a" {
		t.Fatalf("switcher origin = %q, want a", got)
	}
	if !home.sessionSwitcher.reattachOnCancel {
		t.Fatal("Esc from an embedded-opened switcher must reattach to the origin session")
	}
	if !home.sessionSwitcher.embeddedOnAttach {
		t.Fatal("embedded-opened switcher did not preserve the embedded attach mode")
	}
	if home.sessionInput.active || home.sessionInput.switchByte != 0 {
		t.Fatal("embedded input routing remained active behind the switcher")
	}
}

func TestEmbeddedCtrlSOverlayKeepsSidebarVisibleAndAlignsToSessionPane(t *testing.T) {
	optInEmbeddedSessionSwitcher(t)
	InitTheme("dark")
	home := NewHome()
	home.embeddedLayout = true
	home.width = 120
	home.height = 40
	home.initialLoading = false
	home.sessionInput = &SessionInputRouter{active: true, switchByte: 0x13}
	home.sessionExists = func(*session.Instance) bool { return true }

	now := time.Now()
	origin := session.NewInstanceWithTool("sidebar-origin", "/tmp/origin", "claude")
	origin.Status = session.StatusRunning
	origin.LastAccessedAt = now
	target := session.NewInstanceWithTool("switch-target", "/tmp/target", "claude")
	target.Status = session.StatusIdle
	target.LastAccessedAt = now.Add(-time.Minute)
	home.instances = []*session.Instance{origin, target}
	home.instanceByID = map[string]*session.Instance{origin.ID: origin, target.ID: target}
	home.groupTree = session.NewGroupTree(home.instances)
	home.rebuildFlatItems()
	if !home.SelectSessionByID(origin.ID) {
		t.Fatal("could not select origin session")
	}
	home.embeddedMode = true
	home.insertMode = true
	home.insertModeSessionID = origin.ID

	_, _ = home.handleInsertModeKey(tea.KeyMsg{Type: tea.KeyCtrlS})
	view := ansi.Strip(home.View())
	leftWidth, _ := home.splitPaneWidths()
	wantCardX := leftWidth + paneSeparatorWidth

	// The picker must be composited over the active-session pane, not returned
	// as a full-screen replacement. Both the sidebar chrome and its selected
	// session remain present to the left of the card.
	leftColumn := make([]string, 0, home.height)
	cardX := -1
	for _, line := range strings.Split(view, "\n") {
		leftColumn = append(leftColumn, ansi.Cut(line, 0, leftWidth))
		if idx := strings.Index(line, "╭"); idx >= 0 && cardX < 0 {
			cardX = cellWidth(line[:idx])
		}
	}
	left := strings.Join(leftColumn, "\n")
	if !strings.Contains(left, "AGENTS") || !strings.Contains(left, "sidebar-origi") {
		t.Fatalf("sidebar or its current selection disappeared behind switcher:\n%s", view)
	}
	if cardX != wantCardX {
		t.Fatalf("switcher card x = %d, want active-session pane x = %d:\n%s", cardX, wantCardX, view)
	}
	if !strings.Contains(view, "Switch session") || !strings.Contains(view, target.Title) {
		t.Fatalf("switcher content missing from dashboard composite:\n%s", view)
	}

	// Deliberate picker navigation must move the underlying sidebar cursor to
	// the same session. That makes the visible sidebar highlight a live map of
	// the quick picker's selection rather than leaving it parked on the origin.
	_, _ = home.handleSessionSwitcherKey(tea.KeyMsg{Type: tea.KeyDown})
	if selected := home.getSelectedSession(); selected == nil || selected.ID != target.ID {
		t.Fatalf("sidebar selection = %v after picker moved, want %s", selected, target.ID)
	}
	mappedView := ansi.Strip(home.View())
	var targetSidebarLine string
	for _, line := range strings.Split(mappedView, "\n") {
		left := ansi.Cut(line, 0, leftWidth)
		if strings.Contains(left, "switch-target") {
			targetSidebarLine = left
			break
		}
	}
	if !strings.Contains(targetSidebarLine, "▶") {
		t.Fatalf("sidebar did not visibly highlight the quick-picker target; line=%q\n%s", targetSidebarLine, mappedView)
	}

	_, _ = home.handleSessionSwitcherKey(tea.KeyMsg{Type: tea.KeyEsc})
	if selected := home.getSelectedSession(); selected == nil || selected.ID != origin.ID {
		t.Fatalf("sidebar selection = %v after picker cancel, want origin %s", selected, origin.ID)
	}
}

func TestOverlayAtCellsKeepsPaneAnchorAfterWideSidebarText(t *testing.T) {
	base := "界abcde"
	got := overlayAtCells(base, "XX", 0, 4)
	if atAnchor := ansi.Cut(got, 4, 6); atAnchor != "XX" {
		t.Fatalf("overlay at cell 4 = %q, want XX (full line %q)", atAnchor, got)
	}
	if gotWidth, wantWidth := cellWidth(got), cellWidth(base); gotWidth != wantWidth {
		t.Fatalf("composite width = %d, want preserved width %d: %q", gotWidth, wantWidth, got)
	}
}

func TestEmbeddedCtrlSCommitKeepsSidebarVisibleAndEmbeddedMode(t *testing.T) {
	for _, sidebarHidden := range []bool{false, true} {
		for _, choice := range []struct {
			name string
			key  tea.KeyType
		}{
			{"enter target", tea.KeyEnter},
			{"escape origin", tea.KeyEsc},
		} {
			t.Run(fmt.Sprintf("sidebar_hidden_%v/%s", sidebarHidden, choice.name), func(t *testing.T) {
				optInEmbeddedSessionSwitcher(t)
				home := NewHome()
				t.Cleanup(home.exitInsertMode)
				home.embeddedLayout = true
				home.width = 120
				home.height = 40
				home.initialLoading = false
				home.sessionInput = &SessionInputRouter{active: true, switchByte: 0x13}
				home.sessionExists = func(*session.Instance) bool { return true }
				home.embeddedSidebarHidden = sidebarHidden

				now := time.Now()
				origin := session.NewInstanceWithTool("origin", "/tmp/origin", "claude")
				origin.Status = session.StatusRunning
				origin.LastAccessedAt = now
				target := session.NewInstanceWithTool("target", "/tmp/target", "claude")
				target.Status = session.StatusIdle
				target.LastAccessedAt = now.Add(-time.Minute)
				home.instances = []*session.Instance{origin, target}
				home.instanceByID = map[string]*session.Instance{origin.ID: origin, target.ID: target}
				home.groupTree = session.NewGroupTree(home.instances)
				home.rebuildFlatItems()
				if !home.SelectSessionByID(origin.ID) {
					t.Fatal("could not select origin session")
				}
				home.embeddedMode = true
				home.insertMode = true
				home.insertModeSessionID = origin.ID

				_, _ = home.handleInsertModeKey(tea.KeyMsg{Type: tea.KeyCtrlS})
				if home.embeddedSidebarHidden {
					t.Fatal("Ctrl+S did not make the sidebar persistent for the next attached session")
				}
				home.sessionSwitcher.lastCycleAt = time.Time{}
				_, commitTimer := home.handleSessionSwitcherKey(tea.KeyMsg{Type: tea.KeyCtrlS})
				if commitTimer != nil {
					t.Fatal("embedded Ctrl+S cycle armed an idle commit")
				}
				if selected := home.getSelectedSession(); selected == nil || selected.ID != target.ID {
					t.Fatalf("Ctrl+S highlight = %v, want target %s", selected, target.ID)
				}
				generation := home.embeddedGeneration
				// A queued timer with the current generation must also be inert,
				// not merely absent from the newly returned command.
				gen := home.sessionSwitcher.commitGen
				if cmd := home.handleSwitcherCommit(switcherCommitMsg{gen: gen}); cmd != nil {
					t.Fatal("embedded idle timer scheduled an attach")
				}
				if !home.sessionSwitcher.IsVisible() || home.embeddedMode || home.insertMode {
					t.Fatal("idle timer left the picker or entered a session before explicit choice")
				}
				if home.embeddedGeneration != generation || home.embeddedRequest.Name != "" {
					t.Fatal("idle timer changed the attach generation or target request")
				}
				if selected := home.getSelectedSession(); selected == nil || selected.ID != target.ID {
					t.Fatalf("idle timer changed pending highlight: %v", selected)
				}
				if home.embeddedSidebarHidden {
					t.Fatal("idle timer hid the sidebar while awaiting explicit choice")
				}
				_, cmd := home.handleSessionSwitcherKey(tea.KeyMsg{Type: choice.key})
				want := target
				if choice.key == tea.KeyEsc {
					want = origin
				}
				if home.sessionSwitcher.IsVisible() {
					t.Fatal("explicit choice did not close the switcher")
				}
				if selected := home.getSelectedSession(); selected == nil || selected.ID != want.ID {
					t.Fatalf("sidebar selection after explicit choice = %v, want %s", selected, want.ID)
				}
				if cmd == nil {
					t.Fatal("embedded switch commit did not schedule the target PTY attach")
				}
				if !home.embeddedMode || !home.insertMode {
					t.Fatal("embedded switch commit fell back to full-screen attach")
				}
				if got := home.insertModeSessionID; got != want.ID {
					t.Fatalf("embedded target = %q, want %q", got, want.ID)
				}
				if home.embeddedSidebarHidden {
					t.Fatal("sidebar returned to hidden after the explicit embedded choice")
				}
				if home.isAttaching.Load() {
					t.Fatal("embedded switch entered the full-screen attaching state")
				}
				if got := home.embeddedRequest.Name; got != want.GetTmuxSession().Name {
					t.Fatalf("embedded attach request = %q, want %q", got, want.GetTmuxSession().Name)
				}
			})
		}
	}
}

func TestEmbeddedRemoteCtrlSRemainsPaneInput(t *testing.T) {
	optInEmbeddedSessionSwitcher(t)
	home := NewHome()
	home.sessionInput = &SessionInputRouter{active: true}
	home.embeddedMode = true
	home.insertMode = true
	home.insertModeRemoteName = "lab"
	home.insertModeRemoteID = "remote-1"

	_, _ = home.handleInsertModeKey(tea.KeyMsg{Type: tea.KeyCtrlS})
	if !home.embeddedMode || !home.insertMode {
		t.Fatal("Ctrl+S must remain pane input for a remote embedded session")
	}
	if home.sessionSwitcher.IsVisible() {
		t.Fatal("local-only session switcher opened from a remote embedded session")
	}
}

func TestEmbeddedPaneRectMatchesRenderedLayout(t *testing.T) {
	home := &Home{width: 100, height: 30, compactSidebar: true, embeddedLayout: true}
	if got, want := home.embeddedPaneRect(), (terminalCellRect{X: 22, Y: 3, Width: 77, Height: 24}); got != want {
		t.Fatalf("dual embedded rect = %#v, want %#v", got, want)
	}

	home.width = 70
	if got, want := home.embeddedPaneRect(), (terminalCellRect{X: 0, Y: 14, Width: 70, Height: 14}); got != want {
		t.Fatalf("stacked embedded rect = %#v, want %#v", got, want)
	}

	home.embeddedSidebarHidden = true
	if got, want := home.embeddedPaneRect(), (terminalCellRect{X: 1, Y: 3, Width: 68, Height: 24}); got != want {
		t.Fatalf("sidebar-hidden embedded rect = %#v, want %#v", got, want)
	}
}

func TestEmbeddedCtrlAltBTogglesSidebarAndPreservesSession(t *testing.T) {
	home, _, _ := armHomeWithOneSession(t)
	home.embeddedLayout = true
	home.sessionInput = &SessionInputRouter{}
	home.embeddedMode = true
	home.insertMode = true

	model, _ := home.Update(tea.KeyMsg{Type: tea.KeyCtrlG})
	home = model.(*Home)
	if !home.embeddedSidebarHidden {
		t.Fatal("Ctrl+Alt+B signal did not hide the embedded sidebar")
	}
	if !home.embeddedMode {
		t.Fatal("sidebar toggle detached the embedded session")
	}
	hiddenFrame := ansi.Strip(home.View())
	if strings.Contains(hiddenFrame, "AGENTS  GROUPED") {
		t.Fatalf("sidebar title remained visible after collapse:\n%s", hiddenFrame)
	}
	if !strings.Contains(hiddenFrame, "show sidebar") {
		t.Fatalf("collapsed session bar does not advertise restore shortcut:\n%s", hiddenFrame)
	}

	model, _ = home.Update(tea.KeyMsg{Type: tea.KeyCtrlG})
	home = model.(*Home)
	if home.embeddedSidebarHidden {
		t.Fatal("second Ctrl+Alt+B signal did not restore the sidebar")
	}
	if !strings.Contains(ansi.Strip(home.View()), "AGENTS  GROUPED") {
		t.Fatal("restored frame is missing the agent sidebar")
	}
}

func TestEmbeddedWindowRowTargetsTheSelectedWindow(t *testing.T) {
	home, inst, _ := armHomeWithOneSession(t)
	home.embeddedLayout = true
	home.sessionExists = func(*session.Instance) bool { return true }
	home.flatItems = []session.Item{{
		Type:            session.ItemTypeWindow,
		WindowSessionID: inst.ID,
		WindowIndex:     3,
		WindowName:      "review",
	}}
	home.cursor = 0

	var opened insertTargetRef
	home.insertOpenKeySender = func(target insertTargetRef) (insertKeySender, error) {
		opened = target
		return &fakeInsertKeySender{}, nil
	}

	model, _ := home.handleMainKey(tea.KeyMsg{Type: tea.KeyEnter})
	home = model.(*Home)
	if !home.embeddedMode {
		t.Fatal("Enter on a window row should activate the embedded session pane")
	}
	if !opened.hasWindow || opened.windowIndex != 3 {
		t.Fatalf("opened target = %#v, want window index 3", opened)
	}
}

func TestEmbeddedCtrlQDetachesAndClosesSender(t *testing.T) {
	home, fake := armEmbeddedSessionWithFakeSender(t)

	model, _ := home.Update(tea.KeyMsg{Type: tea.KeyCtrlQ})
	home = model.(*Home)

	if home.embeddedMode || home.insertMode {
		t.Fatal("Ctrl+Q should return focus to the session list")
	}
	if got := fake.closeCount.Load(); got != 1 {
		t.Fatalf("sender Close calls = %d, want 1", got)
	}
}

func TestEmbeddedCtrlArrowsScrollSessionHistory(t *testing.T) {
	home, fake := armEmbeddedSessionWithFakeSender(t)

	model, _ := home.Update(tea.KeyMsg{Type: tea.KeyCtrlUp})
	home = model.(*Home)
	if home.previewScrollOffset != embeddedScrollStep {
		t.Fatalf("Ctrl+Up scroll offset = %d, want %d", home.previewScrollOffset, embeddedScrollStep)
	}
	if got := fake.namedKeyCount.Load(); got != 0 {
		t.Fatalf("Ctrl+Up was injected into the pane %d time(s); embedded scrollback should consume it", got)
	}

	model, _ = home.Update(tea.KeyMsg{Type: tea.KeyCtrlDown})
	home = model.(*Home)
	if home.previewScrollOffset != 0 {
		t.Fatalf("Ctrl+Down scroll offset = %d, want 0", home.previewScrollOffset)
	}
}

func TestEmbeddedTerminalScrollOffsetShowsOlderLines(t *testing.T) {
	preview := strings.Join([]string{"line-1", "line-2", "line-3", "line-4", "line-5", "line-6"}, "\n")
	tail := ansi.Strip(renderEmbeddedTerminal("agent", "running", "claude", preview, 40, 5, 0))
	scrolled := ansi.Strip(renderEmbeddedTerminal("agent", "running", "claude", preview, 40, 5, 2))
	if strings.Contains(tail, "line-2") || !strings.Contains(tail, "line-6") {
		t.Fatalf("tail view selected wrong lines: %q", tail)
	}
	if !strings.Contains(scrolled, "line-2") || strings.Contains(scrolled, "line-6") {
		t.Fatalf("scrolled view did not move into history: %q", scrolled)
	}
}

func TestEmbeddedEscapeIsForwardedWithoutDetaching(t *testing.T) {
	home, fake := armEmbeddedSessionWithFakeSender(t)

	model, _ := home.Update(tea.KeyMsg{Type: tea.KeyEsc})
	home = model.(*Home)

	if !home.embeddedMode {
		t.Fatal("Esc belongs to the embedded application and must not detach")
	}
	if got := fake.namedKeyCount.Load(); got != 1 {
		t.Fatalf("named-key sends = %d, want Esc forwarded once", got)
	}
}

func TestEmbeddedForwardsTerminalNavigationAndFunctionKeys(t *testing.T) {
	tests := []struct {
		name string
		key  tea.KeyType
		alt  bool
	}{
		{name: "home", key: tea.KeyHome},
		{name: "end", key: tea.KeyEnd},
		{name: "page up", key: tea.KeyPgUp},
		{name: "page down", key: tea.KeyPgDown},
		{name: "delete", key: tea.KeyDelete},
		{name: "insert", key: tea.KeyInsert},
		{name: "function key", key: tea.KeyF5},
		{name: "control chord", key: tea.KeyCtrlL},
		{name: "alt arrow", key: tea.KeyUp, alt: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			home, fake := armEmbeddedSessionWithFakeSender(t)
			_, _ = home.Update(tea.KeyMsg{Type: tt.key, Alt: tt.alt})
			if got := fake.namedKeyCount.Load(); got != 1 {
				t.Fatalf("named-key sends = %d, want 1", got)
			}
		})
	}
}
