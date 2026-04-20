package ui

import (
	"errors"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
)

func stubQueryFn(paths []string, err error) zoxideQueryFunc {
	return func(string) ([]string, error) {
		return paths, err
	}
}

func feedRune(t *testing.T, z *ZoxidePicker, r rune) {
	t.Helper()
	z, _ = z.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{r}})
	_ = z
}

func TestZoxidePicker_ShowPopulatesResults(t *testing.T) {

	paths := []string{"/home/me/a", "/home/me/b"}
	z := newZoxidePickerWithQueryFn(stubQueryFn(paths, nil))

	z.Show()

	if !z.IsVisible() {
		t.Fatal("expected picker to be visible")
	}
	if got := z.Selected(); got != "/home/me/a" {
		t.Fatalf("Selected() = %q, want /home/me/a", got)
	}
}

func TestZoxidePicker_TypingRefreshesResults(t *testing.T) {
	calls := 0
	fn := func(q string) ([]string, error) {
		calls++
		if q == "" {
			return []string{"/first/path"}, nil
		}
		return []string{"/queried/" + q}, nil
	}
	z := newZoxidePickerWithQueryFn(fn)
	z.Show()
	preTypingCalls := calls

	feedRune(t, z, 'x')

	if calls != preTypingCalls+1 {
		t.Fatalf("expected one refresh per keystroke, got %d (was %d)", calls, preTypingCalls)
	}
	if got := z.Selected(); got != "/queried/x" {
		t.Fatalf("Selected() = %q, want /queried/x", got)
	}
}

func TestZoxidePicker_CursorNavClamped(t *testing.T) {
	z := newZoxidePickerWithQueryFn(stubQueryFn([]string{"/a", "/b", "/c"}, nil))
	z.Show()

	z, _ = z.Update(tea.KeyMsg{Type: tea.KeyDown})
	z, _ = z.Update(tea.KeyMsg{Type: tea.KeyDown})
	z, _ = z.Update(tea.KeyMsg{Type: tea.KeyDown}) // past end
	if got := z.Selected(); got != "/c" {
		t.Fatalf("after 3 downs Selected() = %q, want /c", got)
	}

	z, _ = z.Update(tea.KeyMsg{Type: tea.KeyUp})
	z, _ = z.Update(tea.KeyMsg{Type: tea.KeyUp})
	z, _ = z.Update(tea.KeyMsg{Type: tea.KeyUp}) // past start
	if got := z.Selected(); got != "/a" {
		t.Fatalf("after 3 ups Selected() = %q, want /a", got)
	}
}

func TestZoxidePicker_QueryErrorSurfaced(t *testing.T) {
	z := newZoxidePickerWithQueryFn(stubQueryFn(nil, errors.New("boom")))

	z.Show()

	if z.Selected() != "" {
		t.Fatalf("expected no selection on error, got %q", z.Selected())
	}
	view := z.View()
	if view == "" {
		t.Fatal("expected non-empty view on error")
	}
}

func TestZoxidePicker_HideClearsVisibility(t *testing.T) {
	z := newZoxidePickerWithQueryFn(stubQueryFn([]string{"/x"}, nil))
	z.Show()

	z.Hide()

	if z.IsVisible() {
		t.Fatal("expected picker hidden")
	}
	if z.View() != "" {
		t.Fatal("hidden picker should render empty")
	}
}

func TestZoxidePicker_NoResultsShowsHint(t *testing.T) {
	z := newZoxidePickerWithQueryFn(stubQueryFn(nil, nil))
	z.SetSize(120, 40)

	z.Show()

	if z.Selected() != "" {
		t.Fatalf("expected no selection with empty results, got %q", z.Selected())
	}
	if z.View() == "" {
		t.Fatal("expected non-empty view even with no results")
	}
}
