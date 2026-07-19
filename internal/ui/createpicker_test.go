package ui

import (
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/asheshgoplani/agent-deck/internal/session"
)

// TestCreatePicker_SuggestProviderPopulatesOnShow verifies that when a
// PathSuggest provider is wired, Show() pre-populates the frecency-ranked
// candidate list (no query typed) — "the picker IS the interaction".
func TestCreatePicker_SuggestProviderPopulatesOnShow(t *testing.T) {
	z := NewZoxidePicker()
	z.SetSuggestProvider(func(q string) []session.PathCandidate {
		return []session.PathCandidate{
			{Path: "/home/u/agent-deck", Source: session.SourceRecent},
			{Path: "/home/u/other", Source: session.SourceGroup},
		}
	})

	z.Show()

	if !z.IsVisible() {
		t.Fatal("picker not visible after Show")
	}
	if got := z.Selected(); got != "/home/u/agent-deck" {
		t.Fatalf("Selected() = %q, want /home/u/agent-deck (top frecency row)", got)
	}
}

// TestCreatePicker_SuggestProviderRefreshesOnType verifies each keystroke
// re-queries the provider so results narrow as the user types.
func TestCreatePicker_SuggestProviderRefreshesOnType(t *testing.T) {
	calls := 0
	z := NewZoxidePicker()
	z.SetSuggestProvider(func(q string) []session.PathCandidate {
		calls++
		if q == "" {
			return []session.PathCandidate{{Path: "/default", Source: session.SourceRecent}}
		}
		return []session.PathCandidate{{Path: "/queried/" + q, Source: session.SourceRecent}}
	})
	z.Show()
	pre := calls

	z, _ = z.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'x'}})

	if calls != pre+1 {
		t.Fatalf("expected one provider refresh per keystroke, got %d (was %d)", calls, pre)
	}
	if got := z.Selected(); got != "/queried/x" {
		t.Fatalf("Selected() = %q, want /queried/x", got)
	}
}

// TestCreatePicker_ShowsSourceHint verifies the rendered list annotates each
// candidate with its source so the user can tell a recent from a zoxide hit.
func TestCreatePicker_ShowsSourceHint(t *testing.T) {
	z := NewZoxidePicker()
	z.SetSize(120, 40)
	z.SetSuggestProvider(func(q string) []session.PathCandidate {
		return []session.PathCandidate{{Path: "/home/u/agent-deck", Source: session.SourceRecent}}
	})
	z.Show()

	view := z.View()
	if !strings.Contains(view, "recent") {
		t.Fatalf("view missing source hint 'recent':\n%s", view)
	}
}

// TestCreatePicker_ProviderBypassesZoxideUnavailable verifies that wiring a
// suggest provider makes the picker usable even when the zoxide binary is
// absent (the provider is the source of truth, zoxide is just one input).
func TestCreatePicker_ProviderBypassesZoxideUnavailable(t *testing.T) {
	z := NewZoxidePicker()
	z.checkAvail = true // simulate the real constructor's availability gate
	z.SetSuggestProvider(func(q string) []session.PathCandidate {
		return []session.PathCandidate{{Path: "/from/provider", Source: session.SourceRecent}}
	})

	z.Show()

	if z.unavail {
		t.Fatal("picker marked unavailable despite a wired suggest provider")
	}
	if got := z.Selected(); got != "/from/provider" {
		t.Fatalf("Selected() = %q, want /from/provider", got)
	}
}
