package ui

import "testing"

// The overview dispatches on the rune the terminal delivers, and a terminal
// never says which keyboard layout produced it. With a Russian layout selected
// the "n" key sends "т", no case matches, and every letter hotkey in the deck
// is dead until you switch layouts. These tests pin the alias table that makes
// the twin reach the same action.

// TestJcukenAliasReachesTheSameAction walks the real resolve -> lookup path a
// key press takes, so a binding table change cannot quietly drop the twins.
func TestJcukenAliasReachesTheSameAction(t *testing.T) {
	lookup, _ := buildHotkeyLookup(resolveHotkeys(nil))

	cases := []struct {
		pressed   string
		canonical string
		action    string
	}{
		{"т", "n", hotkeyNewSession},
		{"щ", "o", hotkeyPromptSession},
		{"ф", "a", hotkeyQuickApprove},
		{"а", "f", hotkeyQuickFork},
		{"в", "d", hotkeyDelete},
		{"ь", "m", hotkeyMCPManager},
		{"ы", "s", hotkeySkillsManager},
		{"г", "u", hotkeyMarkUnread},
		{"й", "q", hotkeyQuit},
	}
	for _, tc := range cases {
		if got := lookup[tc.pressed]; got != tc.canonical {
			t.Errorf("%s: pressing %q resolved to %q, want %q", tc.action, tc.pressed, got, tc.canonical)
		}
	}
}

// TestJcukenAliasCoversShiftedBindings pins the two shifted spellings: a bare
// uppercase default (F) and the "shift+<letter>" form (unarchive_session).
func TestJcukenAliasCoversShiftedBindings(t *testing.T) {
	lookup, _ := buildHotkeyLookup(resolveHotkeys(nil))

	if got := lookup["А"]; got != "F" {
		t.Errorf("fork_with_options: pressing %q resolved to %q, want %q", "А", got, "F")
	}
	if got := lookup["Г"]; got != "shift+u" {
		t.Errorf("unarchive_session: pressing %q resolved to %q, want %q", "Г", got, "shift+u")
	}
}

// TestLatinBindingsStillResolve is the regression pin: adding twins must not
// displace the Latin keys everyone already has in their fingers.
func TestLatinBindingsStillResolve(t *testing.T) {
	lookup, _ := buildHotkeyLookup(resolveHotkeys(nil))

	for _, key := range []string{"n", "o", "a", "f", "d", "m", "s", "u", "q", "F", "shift+u"} {
		if _, ok := lookup[key]; !ok {
			t.Errorf("latin binding %q no longer resolves", key)
		}
	}
}

// TestJcukenAliasesAreUnambiguous proves the twins cannot collide: two actions
// resolving to one pressed key would make a hotkey mean two things at once.
func TestJcukenAliasesAreUnambiguous(t *testing.T) {
	bindings := resolveHotkeys(nil)

	owner := make(map[string]string)
	for _, action := range hotkeyActionOrder {
		bound, ok := bindings[action]
		if !ok {
			continue
		}
		for _, alias := range hotkeyAliases(bound) {
			if previous, seen := owner[alias]; seen && previous != action {
				t.Errorf("alias %q is claimed by both %q and %q", alias, previous, action)
				continue
			}
			owner[alias] = action
		}
	}
}

// TestRemappedActionReleasesItsJcukenTwin pins the other half of the contract:
// moving an action off its default key must free the twin as well, or the old
// Cyrillic key would keep firing an action the user deliberately moved.
func TestRemappedActionReleasesItsJcukenTwin(t *testing.T) {
	lookup, blocked := buildHotkeyLookup(resolveHotkeys(map[string]string{
		hotkeyNewSession: "ctrl+n",
	}))

	if got, ok := lookup["т"]; ok {
		t.Errorf("remapped new_session still answers %q (resolved to %q)", "т", got)
	}
	if !blocked["n"] {
		t.Errorf("remapped new_session left its canonical key %q unblocked", "n")
	}
	if got := lookup["ctrl+n"]; got != "n" {
		t.Errorf("new_session on ctrl+n resolved to %q, want %q", got, "n")
	}
}

// TestLayoutAliasForIgnoresChords keeps the table off anything that is not a
// single key: a chord or an already-Cyrillic binding has no twin to add.
func TestLayoutAliasForIgnoresChords(t *testing.T) {
	for _, key := range []string{"", "ctrl+z", "shift+u", "alt+a", "pageup", "/", "!", "т"} {
		if got := layoutAliasFor(key); got != "" {
			t.Errorf("layoutAliasFor(%q) = %q, want empty", key, got)
		}
	}
}

// TestExplicitCyrillicBindingBeatsDerivedTwin pins precedence: a key the user
// wrote into [hotkeys] belongs to that action, even when it is the layout twin
// of an earlier action's default. Before the two-pass lookup new_session's
// derived "т" claimed the key first, quick_fork's canonical "f" was blocked by
// the remap, and quick_fork was unreachable with no warning.
func TestExplicitCyrillicBindingBeatsDerivedTwin(t *testing.T) {
	h := NewHome()
	h.setHotkeys(resolveHotkeys(map[string]string{hotkeyQuickFork: "т"}))

	if got := h.normalizeMainKey("т"); got != "f" {
		t.Errorf("quick_fork bound to %q resolved to %q, want %q (derived new_session twin shadows the explicit binding)", "т", got, "f")
	}
	if got := h.normalizeMainKey("f"); got != "" {
		t.Errorf("remapped quick_fork left its canonical key %q live (resolved to %q)", "f", got)
	}
	if got := h.normalizeMainKey("n"); got != "n" {
		t.Errorf("new_session's canonical key %q resolved to %q, want %q", "n", got, "n")
	}
}

// TestExplicitBindingOnEarlierActionBeatsLaterTwin is the reverse order: the
// explicit binding sits on an action that comes before the twin's owner in
// hotkeyActionOrder. The later action's derived twin must yield, and its
// canonical key must keep working because a derived twin never blocks one.
func TestExplicitBindingOnEarlierActionBeatsLaterTwin(t *testing.T) {
	h := NewHome()
	h.setHotkeys(resolveHotkeys(map[string]string{hotkeyNewSession: "а"}))

	if got := h.normalizeMainKey("а"); got != "n" {
		t.Errorf("new_session bound to %q resolved to %q, want %q", "а", got, "n")
	}
	if got := h.normalizeMainKey("f"); got != "f" {
		t.Errorf("quick_fork's canonical key %q resolved to %q, want %q", "f", got, "f")
	}
	if got := h.normalizeMainKey("n"); got != "" {
		t.Errorf("remapped new_session left its canonical key %q live (resolved to %q)", "n", got)
	}
}
