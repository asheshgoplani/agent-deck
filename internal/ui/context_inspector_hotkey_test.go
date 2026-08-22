package ui

import "testing"

// TestContextInspectorHotkeyRuling pins the hotkey relocation this feature
// required: "C" opens the context inspector, and the preview-info copy it
// displaced (#791) moved to "B". Both are rebindable actions rather than
// hardcoded switch literals, which is the repository's own convention and the
// only way a user can put the old binding back.
func TestContextInspectorHotkeyRuling(t *testing.T) {
	bindings := resolveHotkeys(nil)

	if got := bindings[hotkeyContextInspector]; got != "C" {
		t.Errorf("default context_inspector binding = %q, want \"C\"", got)
	}
	if got := bindings[hotkeyCopyInfo]; got != "B" {
		t.Errorf("default copy_info binding = %q, want \"B\"", got)
	}

	// The relocation must not disturb the rest of the copy family.
	for action, want := range map[string]string{
		hotkeyCopyOutput:    "c",
		hotkeyCopyPane:      "V",
		hotkeyWorktreeSetup: "b",
	} {
		if got := bindings[action]; got != want {
			t.Errorf("%s binding = %q, want %q", action, got, want)
		}
	}

	// No two actions may share a key: a collision means one of them is
	// unreachable, silently, with no compile error.
	seen := make(map[string]string, len(bindings))
	for action, key := range bindings {
		if other, dup := seen[key]; dup {
			t.Errorf("actions %q and %q both bind %q", other, action, key)
		}
		seen[key] = action
	}
}

func TestContextInspectorHotkeyIsRebindableAndUnbindable(t *testing.T) {
	overridden := resolveHotkeys(map[string]string{"context_inspector": "ctrl+g"})
	if got := overridden[hotkeyContextInspector]; got != "ctrl+g" {
		t.Errorf("overridden context_inspector binding = %q, want \"ctrl+g\"", got)
	}

	unbound := resolveHotkeys(map[string]string{"context_inspector": ""})
	if _, ok := unbound[hotkeyContextInspector]; ok {
		t.Error("context_inspector should be absent when explicitly unbound")
	}

	// A user who preferred the old layout can swap the two back.
	swapped := resolveHotkeys(map[string]string{"copy_info": "C", "context_inspector": "B"})
	if got := swapped[hotkeyCopyInfo]; got != "C" {
		t.Errorf("swapped copy_info binding = %q, want \"C\"", got)
	}
	if got := swapped[hotkeyContextInspector]; got != "B" {
		t.Errorf("swapped context_inspector binding = %q, want \"B\"", got)
	}
}

// TestContextInspectorHotkeyNormalization verifies the pressed key reaches the
// canonical switch case. The main-view switch dispatches on the canonical
// default key, so a rebound trigger must normalize onto it and the vacated
// default must be blocked.
func TestContextInspectorHotkeyNormalization(t *testing.T) {
	h := &Home{}
	h.setHotkeys(resolveHotkeys(nil))

	for _, pressed := range []string{"C", "shift+c"} {
		if got := h.normalizeMainKey(pressed); got != defaultHotkeyBindings[hotkeyContextInspector] {
			t.Errorf("%q normalized to %q, want %q", pressed, got, defaultHotkeyBindings[hotkeyContextInspector])
		}
	}
	for _, pressed := range []string{"B", "shift+b"} {
		if got := h.normalizeMainKey(pressed); got != defaultHotkeyBindings[hotkeyCopyInfo] {
			t.Errorf("%q normalized to %q, want %q", pressed, got, defaultHotkeyBindings[hotkeyCopyInfo])
		}
	}

	h.setHotkeys(resolveHotkeys(map[string]string{"context_inspector": "ctrl+g"}))
	if got := h.normalizeMainKey("ctrl+g"); got != defaultHotkeyBindings[hotkeyContextInspector] {
		t.Errorf("rebound trigger normalized to %q, want the canonical %q", got, defaultHotkeyBindings[hotkeyContextInspector])
	}
	if got := h.normalizeMainKey("C"); got != "" {
		t.Errorf("the vacated default key should be blocked after a rebind, got %q", got)
	}

	h.setHotkeys(resolveHotkeys(map[string]string{"context_inspector": ""}))
	if got := h.normalizeMainKey("C"); got != "" {
		t.Errorf("an unbound inspector must not dispatch, got %q", got)
	}
}

// TestContextInspectorInHotkeyActionOrder keeps both actions in the ordered
// list, which is what makes them appear in the help panel and in the settings
// surfaces that iterate it.
func TestContextInspectorInHotkeyActionOrder(t *testing.T) {
	want := map[string]bool{hotkeyContextInspector: false, hotkeyCopyInfo: false}
	for _, action := range hotkeyActionOrder {
		if _, ok := want[action]; ok {
			want[action] = true
		}
	}
	for action, found := range want {
		if !found {
			t.Errorf("%s is missing from hotkeyActionOrder", action)
		}
	}
}
