package ui

import (
	"strings"
	"testing"

	"github.com/asheshgoplani/agent-deck/internal/session"
)

func TestNewSearch(t *testing.T) {
	s := NewSearch()

	if s == nil {
		t.Fatal("NewSearch returned nil")
	}
	if s.IsVisible() {
		t.Error("Search should not be visible by default")
	}
	if s.cursor != 0 {
		t.Error("Cursor should start at 0")
	}
}

func TestSearchSetItems(t *testing.T) {
	s := NewSearch()
	items := []*session.Instance{
		{Title: "session-1", ProjectPath: "/tmp/1", Tool: "claude"},
		{Title: "session-2", ProjectPath: "/tmp/2", Tool: "shell"},
	}

	s.SetItems(items)

	if len(s.allItems) != 2 {
		t.Errorf("Expected 2 items, got %d", len(s.allItems))
	}
}

func TestSearchVisibility(t *testing.T) {
	s := NewSearch()

	s.Show()
	if !s.IsVisible() {
		t.Error("Search should be visible after Show()")
	}

	s.Hide()
	if s.IsVisible() {
		t.Error("Search should not be visible after Hide()")
	}
}

func TestSearchSelected(t *testing.T) {
	s := NewSearch()
	items := []*session.Instance{
		{Title: "session-1"},
		{Title: "session-2"},
	}
	s.SetItems(items)
	s.Show()

	selected := s.Selected()
	if selected == nil {
		t.Fatal("Selected should not be nil when items exist")
	}
	if selected.Title != "session-1" {
		t.Errorf("Expected session-1, got %s", selected.Title)
	}
}

func TestSearchSetSize(t *testing.T) {
	s := NewSearch()
	s.SetSize(100, 50)

	if s.width != 100 {
		t.Errorf("Width = %d, want 100", s.width)
	}
	if s.height != 50 {
		t.Errorf("Height = %d, want 50", s.height)
	}
}

func TestSearchView(t *testing.T) {
	s := NewSearch()

	// Not visible - should return empty
	view := s.View()
	if view != "" {
		t.Error("View should be empty when not visible")
	}

	// Visible - should return content
	s.SetSize(80, 24)
	s.Show()
	view = s.View()
	if view == "" {
		t.Error("View should not be empty when visible")
	}
}

// searchBoxBorderWidth returns the cell-width of the input box's top border.
// The first "╭" in the view belongs to the outer overlay box; the second is
// the input box's own opening corner.
func searchBoxBorderWidth(t *testing.T, view string) int {
	t.Helper()
	seen := 0
	for _, line := range strings.Split(view, "\n") {
		if !strings.Contains(line, "╭") {
			continue
		}
		seen++
		if seen < 2 {
			continue
		}
		// A corrupted render splits the closing corner onto its own line.
		if !strings.Contains(line, "╮") {
			t.Fatalf("search box border corrupted: corner split onto its own line: %q", line)
		}
		return cellWidth(strings.TrimLeft(line, " "))
	}
	t.Fatal("search box top border not found in view")
	return 0
}

// TestSearchInputBoxFixedWidth is a regression test for the search overlay
// input box corrupting (its rounded corners splitting onto their own lines)
// once a short query narrows the rendered content below the empty
// placeholder's width. The box must stay a single closed rectangle at a
// fixed width regardless of query length.
func TestSearchInputBoxFixedWidth(t *testing.T) {
	s := NewSearch()
	s.SetSize(200, 50)
	s.Show()

	emptyView := s.View()
	emptyWidth := searchBoxBorderWidth(t, emptyView)

	s.input.SetValue("car")
	s.updateResults()

	carView := s.View()
	carWidth := searchBoxBorderWidth(t, carView)

	if carWidth != emptyWidth {
		t.Errorf("search box width changed with query length: empty=%d car=%d, want equal (fixed width)", emptyWidth, carWidth)
	}

	for _, line := range strings.Split(carView, "\n") {
		switch strings.TrimSpace(line) {
		case "╭", "╮", "╰", "╯":
			t.Errorf("search box corner rendered alone on its own line: %q", line)
		}
	}
}
