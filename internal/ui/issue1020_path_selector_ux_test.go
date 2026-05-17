package ui

import (
	"testing"

	tea "github.com/charmbracelet/bubbletea"
)

// Issue #1020 (@JMBattista): the path-suggestions popup reacts to Up/Down
// arrows too aggressively. Once focus lands on the path field with a
// pre-filled value (the soft-selected state), the popup is visible and the
// auto-activation added in PR #983 makes EVERY first Up/Down arrow get
// swallowed by the popup. The user can never move the cursor up or down OUT
// of the path section to other dialog fields.
//
// Pre-#983 contract (and the intent restored here):
//   - When the path field is soft-selected (Tab-landed, value present,
//     pathInput blurred), Up/Down navigate between form fields. Space or
//     Right explicitly enters popup-nav mode.
//   - When the user is actively editing the path (pathInput focused, value
//     being typed), arrows auto-activate the popup so paskal's #896 sub-bugs
//     3+4 still work — see [[issue896_residual_test]].
//
// This test pins the soft-selected / form-navigation behavior so #983's
// auto-activation cannot re-eat arrows on the soft-selected path field.
func TestNewDialog_PathSelector_UpDownEscapesField_RegressionFor1020(t *testing.T) {
	d := NewNewDialog()
	d.Show()

	// Seed suggestions so the popup is visible on the path field.
	d.SetPathSuggestions([]string{"/p/alpha", "/p/beta", "/p/gamma"})

	// Simulate the user Tab-landing on a path field that already has a
	// pre-filled value (the common case: cwd or last-used path). updateFocus
	// puts this in soft-select state — pathInput blurred, pathSoftSelected
	// true — which is exactly @JMBattista's scenario.
	d.pathInput.SetValue("/some/preset/path")
	d.focusIndex = 2 // focusPath in the default layout (Name, MultiRepo, Path, ...)
	d.updateFocus()

	if !d.pathSoftSelected {
		t.Fatalf("test precondition: path with non-empty value should be soft-selected on focus")
	}
	if d.suggestionsHidden {
		t.Fatalf("test precondition: popup should be visible (not dismissed) at the start of #1020 scenario")
	}
	startTarget := d.currentTarget()
	if startTarget != focusPath {
		t.Fatalf("test precondition: focus should be on path field, got %v", startTarget)
	}

	// === Press Down: must escape to NEXT form field, NOT activate the popup. ===
	d, _ = d.Update(tea.KeyMsg{Type: tea.KeyDown})

	if d.suggestionsActive {
		t.Fatalf("Down on a soft-selected path field auto-activated the popup; this is issue #1020 — arrows must not eat focus when the user hasn't explicitly entered popup-nav mode")
	}
	if d.currentTarget() == focusPath {
		t.Fatalf("Down on a soft-selected path field did not move focus to the next form field (still on focusPath); issue #1020 — the path popup is trapping arrow keys")
	}
	afterDownTarget := d.currentTarget()

	// === Now back to path, press Up: must escape to PREVIOUS form field. ===
	d.focusIndex = 2
	d.updateFocus()
	if d.currentTarget() != focusPath || !d.pathSoftSelected {
		t.Fatalf("test setup: failed to return to soft-selected path field for Up-arrow check")
	}

	d, _ = d.Update(tea.KeyMsg{Type: tea.KeyUp})

	if d.suggestionsActive {
		t.Fatalf("Up on a soft-selected path field auto-activated the popup; this is issue #1020")
	}
	if d.currentTarget() == focusPath {
		t.Fatalf("Up on a soft-selected path field did not move focus to the previous form field (still on focusPath); issue #1020")
	}
	afterUpTarget := d.currentTarget()

	// Sanity: Down and Up landed on different targets — proves focus moved
	// in opposite directions and the popup didn't just no-op.
	if afterDownTarget == afterUpTarget {
		t.Fatalf("Down and Up from path field landed on the same target %v; expected different neighbors", afterDownTarget)
	}

	// === Popup-nav must still be reachable via explicit gesture. ===
	// Re-focus the path field, press Space (the documented explicit entry
	// from soft-select per newdialog.go ~line 1173). Popup activates. Then
	// Down advances the popup cursor — paskal's #896 sub-bug 4 behavior,
	// just gated behind an explicit gesture.
	d.focusIndex = 2
	d.updateFocus()
	if d.currentTarget() != focusPath || !d.pathSoftSelected {
		t.Fatalf("test setup: failed to return to soft-selected path field for popup-entry check")
	}

	d, _ = d.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{' '}})

	if !d.suggestionsActive {
		t.Fatalf("Space on a soft-selected path field must activate the popup (explicit entry gesture); IsSuggestionsActive()=false")
	}
	startCursor := d.pathSuggestionCursor

	d, _ = d.Update(tea.KeyMsg{Type: tea.KeyDown})

	if !d.suggestionsActive {
		t.Fatalf("after explicit Space-entry, popup deactivated on Down; popup-nav must stay engaged once entered")
	}
	if d.pathSuggestionCursor == startCursor {
		t.Fatalf("after explicit Space-entry, Down did not advance the popup cursor (still at %d); popup arrows must navigate once popup is engaged", d.pathSuggestionCursor)
	}
	if d.currentTarget() != focusPath {
		t.Fatalf("popup-nav Down moved focus off the path field to %v; arrows scoped to popup must keep focus on path", d.currentTarget())
	}
}
