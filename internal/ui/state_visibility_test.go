package ui

import (
	"strings"
	"testing"

	"github.com/asheshgoplani/agent-deck/internal/session"
	"github.com/asheshgoplani/agent-deck/internal/tmux"
)

func TestStateVisibilityFilterBarAt80Columns(t *testing.T) {
	for _, tt := range []struct {
		filter session.Status
		label  string
	}{
		{session.StatusRunning, "Running"},
		{session.StatusWaiting, "Waiting"},
		{session.StatusIdle, "Idle"},
		{session.StatusStopped, "Stopped"},
		{session.StatusError, "Error"},
		{FilterModeActive, "Open"},
		{FilterModeArchived, "Archived"},
	} {
		h := NewHome()
		h.width = 80
		h.statusFilter = tt.filter
		h.timeFilter = session.TimeFilterToday
		h.groupViewMode = session.GroupViewActiveTop
		bar := tmux.StripANSI(h.renderFilterBar())
		if !strings.Contains(bar, tt.label) || !strings.Contains(bar, "Today") || !strings.Contains(bar, "Active top") {
			t.Errorf("filter %q hidden at 80 columns: %q", tt.filter, bar)
		}
		if cellWidth(bar) > h.width {
			t.Errorf("filter %q overflows: %q", tt.filter, bar)
		}
	}
}

func TestStateVisibilityEmptyViews(t *testing.T) {
	for _, tt := range []struct {
		name, want string
		filter     session.Status
		time       session.TimeFilterMode
		hint       string
	}{
		{"archived", "No archived sessions", FilterModeArchived, session.TimeFilterAll, "back to active"},
		{"status", "No sessions match", session.StatusWaiting, session.TimeFilterAll, "0 show all"},
		{"time", "No sessions match", "", session.TimeFilterToday, "* time range"},
		{"status and time", "No sessions match", session.StatusError, session.TimeFilterToday, "* time range"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			h := NewHome()
			h.statusFilter, h.timeFilter = tt.filter, tt.time
			for _, pane := range []string{h.renderSessionList(36, 20), h.renderPreviewPane(40, 20)} {
				plain := tmux.StripANSI(pane)
				if !strings.Contains(plain, tt.want) || strings.Contains(plain, "No Sessions Yet") || strings.Contains(plain, "Ready to Go") {
					t.Errorf("wrong empty view: %q", plain)
				}
				if !strings.Contains(plain, tt.hint) {
					t.Errorf("missing recovery hint %q: %q", tt.hint, plain)
				}
				if tt.name == "time" && strings.Contains(plain, "0 show all") {
					t.Errorf("time-only filter advertises status key: %q", plain)
				}
				if tt.name == "status and time" && !strings.Contains(plain, "0 show all") {
					t.Errorf("combined filter omits status key: %q", plain)
				}
			}
		})
	}
}

func TestStateVisibilityListCardAcrossWidths(t *testing.T) {
	for _, width := range []int{28, 42, 70} { // List panes at 80, 120, and 200 columns.
		for _, tc := range []struct {
			name   string
			status session.Status
			time   session.TimeFilterMode
			want   []string
		}{
			{"status", session.StatusError, session.TimeFilterAll, []string{"Error", "0 show all"}},
			{"time", "", session.TimeFilterToday, []string{"Today", "* time range"}},
			{"both", session.StatusError, session.TimeFilterToday, []string{"Error", "Today", "0 show all", "* time range"}},
		} {
			t.Run(tc.name, func(t *testing.T) {
				h := NewHome()
				h.statusFilter, h.timeFilter = tc.status, tc.time
				list := tmux.StripANSI(h.renderSessionList(width, 18))
				for _, want := range tc.want {
					if !strings.Contains(list, want) {
						t.Errorf("%d-column list card lost %q: %q", width, want, list)
					}
				}
				if strings.Contains(list, "...") {
					t.Errorf("%d-column list card truncated content: %q", width, list)
				}
			})
		}
	}
}

func TestStateVisibilityFullFooterPinsHelp(t *testing.T) {
	h := NewHome()
	h.width, h.height = 120, 40
	h.flatItems = []session.Item{{Type: session.ItemTypeSession, Session: &session.Instance{ID: "s1", Tool: "claude", Status: session.StatusRunning}}}
	h.cursor = 0
	footer := tmux.StripANSI(h.renderHelpBarFull())
	if !strings.Contains(footer, "? Help") || !strings.Contains(footer, "↑↓ Nav") {
		t.Fatalf("120-column footer hid Help or Nav: %q", footer)
	}
	for _, line := range strings.Split(footer, "\n") {
		if cellWidth(line) > h.width {
			t.Fatalf("footer exceeds 120 columns: %q", line)
		}
	}
	h.flatItems = nil
	footer = tmux.StripANSI(h.renderHelpBarFull())
	if strings.Index(footer, "S Settings") < 0 || strings.Index(footer, "? Help") < strings.Index(footer, "S Settings") {
		t.Fatalf("120-column empty footer changed hints that already fit: %q", footer)
	}
	h.flatItems = []session.Item{{Type: session.ItemTypeSession, Session: &session.Instance{ID: "s1", Tool: "claude", Status: session.StatusRunning}}}
	h.width = 200
	footer = tmux.StripANSI(h.renderHelpBarFull())
	if strings.Index(footer, "S Settings") > strings.Index(footer, "? Help") || strings.Index(footer, "C Copy") > strings.Index(footer, "V Copy pane") {
		t.Fatalf("wide footer changed base hint order: %q", footer)
	}
}
