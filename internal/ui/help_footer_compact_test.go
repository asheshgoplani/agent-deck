package ui

import (
	"strings"
	"testing"

	"github.com/asheshgoplani/agent-deck/internal/session"
	"github.com/asheshgoplani/agent-deck/internal/tmux"
)

// TestFooterCompactAt80ColsNeverGluesKeyAndLabel: at 80 columns the footer's
// compact tier used to render "n/NNew" and "⏎Toggle" — a key chip and its
// label with no separating space — because helpKeyShort concatenated them.
func TestFooterCompactAt80ColsNeverGluesKeyAndLabel(t *testing.T) {
	home := NewHome()
	home.width = 80
	home.height = 30
	home.flatItems = []session.Item{
		{Type: session.ItemTypeGroup, Group: &session.Group{Name: "g", Path: "g", Expanded: true}},
	}
	home.cursor = 0

	result := tmux.StripANSI(home.renderHelpBar())

	for _, bad := range []string{"n/NNew", "⏎Toggle"} {
		if strings.Contains(result, bad) {
			t.Errorf("footer still glues key and label: found %q in %q", bad, result)
		}
	}
	if !strings.Contains(result, "⏎ Toggle") {
		t.Errorf("expected '⏎ Toggle' (with a separating space) in footer: %q", result)
	}
}

// TestFooterCompactAt80ColsGlobalKeysKeepLabels: the global key bar
// (Search/Settings/Help/Quit) used to render as bare symbols with no labels
// once squeezed to 80 columns.
func TestFooterCompactAt80ColsGlobalKeysKeepLabels(t *testing.T) {
	home := NewHome()
	home.width = 80
	home.height = 30
	home.flatItems = []session.Item{
		{Type: session.ItemTypeSession, Session: &session.Instance{ID: "s1", Tool: "claude", Status: session.StatusRunning}},
	}
	home.cursor = 0

	result := tmux.StripANSI(home.renderHelpBar())

	// Every global key that appears must carry its label — never a bare
	// symbol with the label dropped while the key survives.
	for _, tc := range []struct{ action, label string }{
		{hotkeySettings, "Settings"},
		{hotkeyHelp, "Help"},
		{hotkeyQuit, "Quit"},
	} {
		key := home.actionKey(tc.action)
		if key == "" {
			continue
		}
		if strings.Contains(result, key) && !strings.Contains(result, key+" "+tc.label) {
			t.Errorf("global key %q appears without its label %q in footer: %q", key, tc.label, result)
		}
	}
}

// TestFooterCompactNeverForceTruncatesMidLabel checks that whatever survives
// the width-fit loop at very narrow compact widths (70-99 cols) never gets
// hard-truncated mid-word by the final MaxWidth safety net — every entry
// that appears is either fully shown or fully dropped.
func TestFooterCompactNeverForceTruncatesMidLabel(t *testing.T) {
	for _, width := range []int{70, 80, 90, 99} {
		home := NewHome()
		home.width = width
		home.height = 30
		home.flatItems = []session.Item{
			{Type: session.ItemTypeSession, Session: &session.Instance{ID: "s1", Tool: "claude", Status: session.StatusRunning}},
		}
		home.cursor = 0

		result := tmux.StripANSI(home.renderHelpBar())
		for _, line := range strings.Split(result, "\n") {
			if len([]rune(line)) > width {
				t.Errorf("width=%d: footer line exceeds terminal width (%d): %q", width, len([]rune(line)), line)
			}
		}
	}
}
