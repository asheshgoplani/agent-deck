package ui

import (
	"testing"

	tea "github.com/charmbracelet/bubbletea"
)

// Issue #896 sub-bug 3 (paskal's enumeration): the path-suggestions popup is
// visible after typing a prefix, but pressing Enter does NOT select the
// highlighted suggestion — it submits whatever value is in the input. The
// popup looks interactive but Enter ignores it.
//
// Root cause: home.go's Enter handler only intercepts when
// IsSuggestionsActive() is true, which today requires the user to first press
// Space or Ctrl+N to enter "arrow-key mode". Plain arrow keys on a freshly
// shown popup do not activate it — they move dialog focus instead.
//
// Fix contract verified here: once the user presses Down on a visible popup,
// the popup becomes active and the highlighted real suggestion is the one
// that gets applied (i.e. ApplyHighlightedSuggestion writes it to pathInput).
// home.go's existing Enter handler then submits with that path.
func TestNewDialog_PopupEnter_SelectsHighlightedSuggestion_RegressionFor896(t *testing.T) {
	d := NewNewDialog()
	d.Show()

	suggestions := []string{"/p/alpha", "/p/beta", "/p/gamma"}
	d.SetPathSuggestions(suggestions)

	// Focus the path field — focusIndex 2 in the default layout
	// (focusName, focusMultiRepo, focusPath, ...).
	d.focusIndex = 2
	d.updateFocus()

	// User has typed a prefix; the popup is visible (path focused, not hidden).
	d.pathInput.SetValue("/p/")

	// User presses Down twice — cursor should advance through:
	//   0 ("Type custom") -> 1 (/p/alpha) -> 2 (/p/beta)
	d, _ = d.Update(tea.KeyMsg{Type: tea.KeyDown})
	d, _ = d.Update(tea.KeyMsg{Type: tea.KeyDown})

	// Precondition for the Enter handler in home.go: popup must be active so
	// IsSuggestionsActive() is true and apply-highlighted runs.
	if !d.IsSuggestionsActive() {
		t.Fatalf("after Down arrows on visible popup, IsSuggestionsActive()=false; home.go's Enter handler will be skipped and form will submit with typed value (issue #896 sub-bug 3)")
	}
	if d.IsTypeCustomHighlighted() {
		t.Fatalf("after 2 Down arrows, Type-custom is highlighted; cursor=%d, expected to be on a real suggestion", d.pathSuggestionCursor)
	}
	if got := d.pathSuggestionCursor; got != 2 {
		t.Fatalf("after 2 Down arrows, pathSuggestionCursor=%d, want 2 (second real suggestion)", got)
	}

	// Now simulate exactly what home.handleNewDialogKey does on Enter when the
	// popup is active and a real suggestion is highlighted:
	//   d.ApplyHighlightedSuggestion(); d.DismissSuggestions();
	// then falls through to the form-submit Enter handler.
	d.ApplyHighlightedSuggestion()
	d.DismissSuggestions()

	got := d.pathInput.Value()
	want := "/p/beta"
	if got != want {
		t.Errorf("after popup-Enter on highlighted suggestion 2, pathInput=%q, want %q\n"+
			"this is issue #896 sub-bug 3: Enter selected the wrong target", got, want)
	}
	if got == "/p/" {
		t.Errorf("popup-Enter kept the typed prefix instead of applying the highlighted suggestion (#896 sub-bug 3)")
	}
	if got == "/p/alpha" {
		t.Errorf("popup-Enter applied suggestion 0 (off-by-one) instead of the highlighted suggestion 2 (#896 sub-bug 3)")
	}
}

// Issue #896 sub-bug 4 (paskal's enumeration): "I am not sure but I think
// sometimes arrows work in pop up, but most of the time and you need to
// operate using ctrl+n and space".
//
// Root cause: when the popup is visible on the path field, Up/Down arrow
// keys are NOT routed to the popup unless suggestionsActive=true. The user
// has to press Space (or Right) first to enter arrow-key mode, otherwise
// arrows move dialog focus between fields. From the user's perspective,
// arrows on a visible popup do nothing useful.
//
// Fix contract: arrows on a visible popup over the path field must navigate
// the popup cursor reliably from press 1, no off-by-one, no skipped frames,
// wrap-around at the ends.
func TestNewDialog_PopupArrows_NavigateReliably_RegressionFor896(t *testing.T) {
	d := NewNewDialog()
	d.Show()

	suggestions := []string{"/p/a", "/p/b", "/p/c"}
	d.SetPathSuggestions(suggestions)

	d.focusIndex = 2 // focusPath
	d.updateFocus()

	// Popup is visible because path is focused, suggestions are non-empty,
	// and suggestionsHidden is false (default after Show + SetPathSuggestions).
	if d.suggestionsHidden {
		t.Fatalf("test precondition: suggestionsHidden=true but popup should be visible")
	}

	// Press Down — cursor space is: 0 ("Type custom"), 1..3 (real suggestions).
	// On a visible popup, Down must navigate the popup; today it advances
	// focusIndex instead, leaving the popup unmoved.
	steps := []struct {
		key      tea.KeyType
		wantCur  int
		wantName string
	}{
		{tea.KeyDown, 1, "down 1 (a)"},
		{tea.KeyDown, 2, "down 2 (b)"},
		{tea.KeyDown, 3, "down 3 (c)"},
		{tea.KeyDown, 0, "down 4 (wrap to Type-custom)"},
		{tea.KeyUp, 3, "up 1 (wrap back to c)"},
		{tea.KeyUp, 2, "up 2 (b)"},
		{tea.KeyUp, 1, "up 3 (a)"},
		{tea.KeyUp, 0, "up 4 (Type-custom)"},
	}

	for i, s := range steps {
		d, _ = d.Update(tea.KeyMsg{Type: s.key})

		if !d.IsSuggestionsActive() {
			t.Fatalf("step %d (%s): popup arrow did not activate suggestions; user has to fall back to Ctrl+N (issue #896 sub-bug 4)", i, s.wantName)
		}
		if d.pathSuggestionCursor != s.wantCur {
			t.Fatalf("step %d (%s): pathSuggestionCursor=%d, want %d (issue #896 sub-bug 4: arrow navigation flaky)",
				i, s.wantName, d.pathSuggestionCursor, s.wantCur)
		}
		// Path field must remain focused — arrows must navigate the popup,
		// not jump to other dialog fields.
		if d.currentTarget() != focusPath {
			t.Fatalf("step %d (%s): focus left the path field (target=%v); popup arrows must stay scoped to the popup", i, s.wantName, d.currentTarget())
		}
	}
}
