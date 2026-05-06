package ui

import (
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"
)

// sampleWatchers returns a list of test watchers for panel tests.
func sampleWatchers() []WatcherDisplayItem {
	return []WatcherDisplayItem{
		{ID: "w1", Name: "slack-watcher", Type: "slack", Status: "running", HealthStatus: "healthy", EventsPerHour: 5.2, Conductor: "main"},
		{ID: "w2", Name: "webhook-watcher", Type: "webhook", Status: "running", HealthStatus: "warning", EventsPerHour: 0.5, Conductor: "main"},
		{ID: "w3", Name: "ntfy-watcher", Type: "ntfy", Status: "stopped", HealthStatus: "stopped", EventsPerHour: 0.0, Conductor: ""},
	}
}

// sampleEvents returns a list of test events for panel detail tests.
func sampleEvents() []WatcherEventDisplay {
	return []WatcherEventDisplay{
		{Timestamp: time.Now().Add(-5 * time.Minute), Sender: "alice@example.com", Subject: "Bug report", RoutedTo: "bug-triage", SessionID: "s1"},
		{Timestamp: time.Now().Add(-15 * time.Minute), Sender: "bob@example.com", Subject: "Feature request", RoutedTo: "feature-inbox", SessionID: "s2"},
	}
}

// TestWatcherPanelShowHide verifies that Show sets visible=true and Hide clears it.
func TestWatcherPanelShowHide(t *testing.T) {
	wp := NewWatcherPanel()

	if wp.IsVisible() {
		t.Fatal("expected panel to be hidden on creation")
	}

	wp.Show()
	if !wp.IsVisible() {
		t.Fatal("expected panel to be visible after Show()")
	}

	wp.Hide()
	if wp.IsVisible() {
		t.Fatal("expected panel to be hidden after Hide()")
	}
}

// TestWatcherPanelShowResetsState verifies that Show resets cursor and detailMode.
func TestWatcherPanelShowResetsState(t *testing.T) {
	wp := NewWatcherPanel()
	wp.SetWatchers(sampleWatchers())
	wp.cursor = 2
	wp.detailMode = true

	wp.Show()

	if wp.cursor != 0 {
		t.Errorf("expected cursor=0 after Show(), got %d", wp.cursor)
	}
	if wp.detailMode {
		t.Error("expected detailMode=false after Show()")
	}
}

// TestWatcherPanelNavigation verifies that Down/Up keys move the cursor.
func TestWatcherPanelNavigation(t *testing.T) {
	wp := NewWatcherPanel()
	wp.SetWatchers(sampleWatchers())
	wp.Show()

	// Move down
	wp, _ = wp.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("j")})
	if wp.cursor != 1 {
		t.Errorf("expected cursor=1 after j, got %d", wp.cursor)
	}

	// Move down again
	wp, _ = wp.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("j")})
	if wp.cursor != 2 {
		t.Errorf("expected cursor=2 after second j, got %d", wp.cursor)
	}

	// Cannot move past last
	wp, _ = wp.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("j")})
	if wp.cursor != 2 {
		t.Errorf("expected cursor to stay at 2 (last), got %d", wp.cursor)
	}

	// Move up
	wp, _ = wp.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("k")})
	if wp.cursor != 1 {
		t.Errorf("expected cursor=1 after k, got %d", wp.cursor)
	}

	// Cannot move before 0
	wp.cursor = 0
	wp, _ = wp.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("k")})
	if wp.cursor != 0 {
		t.Errorf("expected cursor to stay at 0, got %d", wp.cursor)
	}
}

// TestWatcherPanelDetailMode verifies that Enter enters detail view.
func TestWatcherPanelDetailMode(t *testing.T) {
	wp := NewWatcherPanel()
	wp.SetWatchers(sampleWatchers())
	wp.Show()

	if wp.detailMode {
		t.Fatal("expected list mode initially")
	}

	// Enter detail mode
	wp, _ = wp.Update(tea.KeyMsg{Type: tea.KeyEnter})
	if !wp.detailMode {
		t.Fatal("expected detailMode=true after Enter")
	}

	// Esc goes back to list
	wp, _ = wp.Update(tea.KeyMsg{Type: tea.KeyEsc})
	if wp.detailMode {
		t.Fatal("expected detailMode=false after Esc in detail view")
	}

	// Panel should still be visible
	if !wp.IsVisible() {
		t.Fatal("expected panel still visible after Esc from detail mode")
	}

	// Second Esc should hide panel
	wp, _ = wp.Update(tea.KeyMsg{Type: tea.KeyEsc})
	if wp.IsVisible() {
		t.Fatal("expected panel to be hidden after Esc from list mode")
	}
}

// TestWatcherPanelActionMsg verifies that s/x/t keys return WatcherActionMsg.
func TestWatcherPanelActionMsg(t *testing.T) {
	wp := NewWatcherPanel()
	wp.SetWatchers(sampleWatchers())
	wp.Show()

	tests := []struct {
		key    string
		action string
	}{
		{"s", "start"},
		{"x", "stop"},
		{"t", "test"},
	}

	for _, tc := range tests {
		t.Run(tc.action, func(t *testing.T) {
			panel := NewWatcherPanel()
			panel.SetWatchers(sampleWatchers())
			panel.Show()

			_, cmd := panel.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(tc.key)})
			if cmd == nil {
				t.Fatalf("expected cmd for action %q, got nil", tc.action)
			}

			msg := cmd()
			actionMsg, ok := msg.(WatcherActionMsg)
			if !ok {
				t.Fatalf("expected WatcherActionMsg, got %T", msg)
			}

			if actionMsg.Action != tc.action {
				t.Errorf("expected action=%q, got %q", tc.action, actionMsg.Action)
			}
			if actionMsg.WatcherID != "w1" {
				t.Errorf("expected WatcherID=w1, got %q", actionMsg.WatcherID)
			}
			if actionMsg.WatcherName != "slack-watcher" {
				t.Errorf("expected WatcherName=slack-watcher, got %q", actionMsg.WatcherName)
			}
		})
	}
}

// TestWatcherPanelSelectedWatcher verifies SelectedWatcher returns nil for empty list.
func TestWatcherPanelSelectedWatcher(t *testing.T) {
	wp := NewWatcherPanel()
	wp.Show()

	if got := wp.SelectedWatcher(); got != nil {
		t.Errorf("expected nil for empty watcher list, got %+v", got)
	}

	wp.SetWatchers(sampleWatchers())
	got := wp.SelectedWatcher()
	if got == nil {
		t.Fatal("expected non-nil after SetWatchers")
	}
	if got.ID != "w1" {
		t.Errorf("expected ID=w1, got %q", got.ID)
	}
}

// TestWatcherPanelViewRendersWithoutPanic verifies View does not panic in list or detail mode.
func TestWatcherPanelViewRendersWithoutPanic(t *testing.T) {
	wp := NewWatcherPanel()
	wp.SetSize(80, 24)
	wp.SetWatchers(sampleWatchers())
	wp.SetEvents(sampleEvents())
	wp.Show()

	// List view
	listView := wp.View()
	if listView == "" {
		t.Error("expected non-empty list view")
	}

	// Detail view
	wp.detailMode = true
	detailView := wp.View()
	if detailView == "" {
		t.Error("expected non-empty detail view")
	}

	// Hidden
	wp.Hide()
	hiddenView := wp.View()
	if hiddenView != "" {
		t.Error("expected empty string when hidden")
	}
}

// TestWatcherPanelTruncate verifies truncateStr prevents layout overflow.
func TestWatcherPanelTruncate(t *testing.T) {
	long := "a very long string that exceeds the column width limit significantly"
	result := truncateStr(long, 20)
	runes := []rune(result)
	if len(runes) > 20 {
		t.Errorf("expected truncated to <=20 runes, got %d", len(runes))
	}

	short := "short"
	if got := truncateStr(short, 20); got != short {
		t.Errorf("expected unchanged short string, got %q", got)
	}
}

// TestWatcherPanelNoActionOnEmptyList verifies no panic or cmd when list is empty.
func TestWatcherPanel_CtrlN_CtrlP_Navigation(t *testing.T) {
	wp := NewWatcherPanel()
	wp.SetWatchers(sampleWatchers())
	wp.Show()

	// ctrl+n moves down.
	wp, _ = wp.Update(tea.KeyMsg{Type: tea.KeyCtrlN})
	if wp.cursor != 1 {
		t.Errorf("ctrl+n: cursor = %d, want 1", wp.cursor)
	}

	wp, _ = wp.Update(tea.KeyMsg{Type: tea.KeyCtrlN})
	if wp.cursor != 2 {
		t.Errorf("ctrl+n x2: cursor = %d, want 2", wp.cursor)
	}

	// Clamps at last item.
	wp, _ = wp.Update(tea.KeyMsg{Type: tea.KeyCtrlN})
	if wp.cursor != 2 {
		t.Errorf("ctrl+n past end: cursor = %d, want 2 (clamped)", wp.cursor)
	}

	// ctrl+p moves up.
	wp, _ = wp.Update(tea.KeyMsg{Type: tea.KeyCtrlP})
	if wp.cursor != 1 {
		t.Errorf("ctrl+p: cursor = %d, want 1", wp.cursor)
	}

	// Clamps at first item.
	wp.cursor = 0
	wp, _ = wp.Update(tea.KeyMsg{Type: tea.KeyCtrlP})
	if wp.cursor != 0 {
		t.Errorf("ctrl+p at top: cursor = %d, want 0 (clamped)", wp.cursor)
	}
}

func TestWatcherPanelNoActionOnEmptyList(t *testing.T) {
	wp := NewWatcherPanel()
	wp.Show()

	for _, key := range []string{"s", "x", "t"} {
		panel := NewWatcherPanel()
		panel.Show()
		_, cmd := panel.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(key)})
		if cmd != nil {
			t.Errorf("expected nil cmd for key %q with empty list, got non-nil", key)
		}
	}
}
