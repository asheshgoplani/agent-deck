package ui

import "testing"

func TestResolvedSwitchByte_Default(t *testing.T) {
	// The in-attach switcher is opt-in: with no override it is unbound, so the
	// attach loop never steals a control byte from the attached program.
	if got := ResolvedSwitchByte(nil); got != 0 {
		t.Errorf("default switch byte = %#x, want 0 (opt-in, unbound)", got)
	}
}

func TestResolvedSwitchByte_OptInEnablesCtrlS(t *testing.T) {
	// Binding the suggested Ctrl+S explicitly re-enables it (user accepts the
	// collision with the attached program).
	if got := ResolvedSwitchByte(map[string]string{"switch_session": "ctrl+s"}); got != 0x13 {
		t.Errorf("opt-in ctrl+s switch byte = %#x, want 0x13", got)
	}
}

func TestSwitchSessionUnboundByDefault(t *testing.T) {
	bindings := resolveHotkeys(nil)
	if key, ok := bindings[hotkeySwitchSession]; ok {
		t.Errorf("switch_session bound to %q by default, want unbound", key)
	}
	// Pressing the canonical key must not normalize to the dispatch token when
	// the action is unbound — otherwise the home-screen switcher would still
	// open.
	lookup, blocked := buildHotkeyLookup(bindings)
	if _, ok := lookup["ctrl+s"]; ok {
		t.Errorf("ctrl+s maps to a canonical action while switch_session is unbound")
	}
	if !blocked["ctrl+s"] {
		t.Errorf("ctrl+s should be blocked while switch_session is unbound")
	}
}

func TestSwitchSessionOptInDispatch(t *testing.T) {
	// Once bound, the chord normalizes to the canonical dispatch token so the
	// home-screen `case "ctrl+s"` arm fires.
	bindings := resolveHotkeys(map[string]string{"switch_session": "ctrl+o"})
	if bindings[hotkeySwitchSession] != "ctrl+o" {
		t.Fatalf("switch_session = %q, want ctrl+o", bindings[hotkeySwitchSession])
	}
	lookup, _ := buildHotkeyLookup(bindings)
	if got := lookup["ctrl+o"]; got != "ctrl+s" {
		t.Errorf("ctrl+o canonical = %q, want ctrl+s dispatch token", got)
	}
}

func TestResolvedSwitchByte_Override(t *testing.T) {
	if got := ResolvedSwitchByte(map[string]string{"switch_session": "ctrl+x"}); got != 'x'-'a'+1 {
		t.Errorf("overridden switch byte = %#x, want ctrl+x", got)
	}
}

func TestResolvedSwitchByte_UnboundOrNonCtrl(t *testing.T) {
	// Unbound -> 0 (disabled).
	if got := ResolvedSwitchByte(map[string]string{"switch_session": ""}); got != 0 {
		t.Errorf("unbound switch byte = %#x, want 0", got)
	}
	// A non-ctrl binding has no portable byte -> 0.
	if got := ResolvedSwitchByte(map[string]string{"switch_session": "ctrl+tab"}); got != 0 {
		t.Errorf("ctrl+tab switch byte = %#x, want 0 (no legacy byte)", got)
	}
}

func TestCtrlByteFromBinding(t *testing.T) {
	cases := map[string]byte{
		"ctrl+s":         0x13,
		"ctrl+a":         0x01,
		"CTRL+S":         0x13, // case-insensitive
		"ctrl+]":         0x1D,
		"ctrl+tab":       0, // no legacy byte
		"ctrl+shift+tab": 0,
		"tab":            0,
		"s":              0,
		"":               0,
	}
	for binding, want := range cases {
		if got := ctrlByteFromBinding(binding); got != want {
			t.Errorf("ctrlByteFromBinding(%q) = %#x, want %#x", binding, got, want)
		}
	}
}
