package ui

import (
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/asheshgoplani/agent-deck/internal/history/model"
	"github.com/asheshgoplani/agent-deck/internal/history/tree"
)

func TestBrowsePanel_ShowHideVisible(t *testing.T) {
	p := NewBrowsePanel()
	if p.IsVisible() {
		t.Fatal("new panel should be hidden")
	}
	p.Show()
	if !p.IsVisible() {
		t.Fatal("Show() should make panel visible")
	}
	p.Hide()
	if p.IsVisible() {
		t.Fatal("Hide() should hide panel")
	}
}

func TestBrowsePanel_EscReturnsNilSelection(t *testing.T) {
	p := NewBrowsePanel()
	p.Show()
	p.SetSize(80, 24)
	_, _, sel := p.Update(tea.KeyMsg{Type: tea.KeyEsc})
	if sel != nil {
		t.Fatal("Esc must not select a session")
	}
	if p.IsVisible() {
		t.Fatal("Esc should hide the panel")
	}
}

// TestBrowsePanel_RenderSmoke exercises the Task 8 render helpers end-to-end
// with a small fixture, without touching real ~/.claude data.
func TestBrowsePanel_RenderSmoke(t *testing.T) {
	p := NewBrowsePanel()
	p.Show()
	p.SetSize(80, 24)

	now := time.Now()
	p.projects = []model.Project{
		{
			Path: "/tmp/browsetest",
			Name: "browsetest",
			Tool: "claude",
			Sessions: []model.Session{
				{
					ID:      "s1",
					Title:   "Fix bug",
					CWD:     "/tmp/browsetest",
					ModTime: now,
					Status:  model.StatusRunningBusy,
				},
			},
		},
	}
	p.rebuildRows()
	if len(p.rows) == 0 {
		t.Fatal("expected at least one row after rebuildRows")
	}
	// Expand the top-level project row so the session row (and its status
	// glyph) becomes visible.
	p.expanded[p.rows[0].Path] = true
	p.rebuildRows()

	out := p.View()
	if out == "" {
		t.Fatal("View() should not be empty")
	}
	if !strings.Contains(out, model.StatusRunningBusy.Glyph()) {
		t.Fatalf("expected status glyph %q in rendered view, got:\n%s", model.StatusRunningBusy.Glyph(), out)
	}
}

// TestBrowsePanel_EnterOnSessionRowReturnsSelection verifies the resume path:
// Enter on a SessionRow returns that session and does not hide the panel.
func TestBrowsePanel_EnterOnSessionRowReturnsSelection(t *testing.T) {
	p := NewBrowsePanel()
	p.Show()
	p.SetSize(80, 24)

	p.projects = []model.Project{
		{
			Path: "/tmp/browsetest2",
			Name: "browsetest2",
			Tool: "claude",
			Sessions: []model.Session{
				{ID: "s1", Title: "Fix bug", CWD: "/tmp/browsetest2", ModTime: time.Now(), Status: model.StatusRecent},
			},
		},
	}
	p.rebuildRows()
	p.expanded[p.rows[0].Path] = true
	p.rebuildRows()

	// Find the session row and move the cursor onto it.
	sessionIdx := -1
	for i, r := range p.rows {
		if r.Kind == tree.SessionRow {
			sessionIdx = i
			break
		}
	}
	if sessionIdx < 0 {
		t.Fatal("expected a SessionRow after expanding the project")
	}
	p.cursor = sessionIdx

	_, _, sel := p.Update(tea.KeyMsg{Type: tea.KeyEnter})
	if sel == nil {
		t.Fatal("Enter on a SessionRow should return the selected session")
	}
	if sel.ID != "s1" {
		t.Fatalf("expected selected session id %q, got %q", "s1", sel.ID)
	}
	if !p.IsVisible() {
		t.Fatal("resuming a session must not hide the panel (caller decides)")
	}
}
