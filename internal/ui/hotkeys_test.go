package ui

import "testing"

func TestResolveHotkeysOverridesAndUnbinds(t *testing.T) {
	bindings := resolveHotkeys(map[string]string{
		"delete":        "backspace",
		"close_session": "",
		"unknown":       "x",
	})

	if got := bindings[hotkeyDelete]; got != "backspace" {
		t.Fatalf("delete binding = %q, want backspace", got)
	}

	if _, ok := bindings[hotkeyCloseSession]; ok {
		t.Fatalf("close_session should be unbound")
	}

	if got := bindings[hotkeyRestart]; got != defaultHotkeyBindings[hotkeyRestart] {
		t.Fatalf("restart binding = %q, want %q", got, defaultHotkeyBindings[hotkeyRestart])
	}
}

func TestResolveHotkeysPrefersCanonicalNameOverLegacyRename(t *testing.T) {
	bindings := resolveHotkeys(map[string]string{
		"toggle_gemini_yolo": "g",
		"toggle_yolo":        "y",
	})

	if got := bindings[hotkeyToggleYolo]; got != "y" {
		t.Fatalf("toggle_yolo binding = %q, want %q", got, "y")
	}
}

func TestResolveHotkeysMapsLegacyRenameWhenCanonicalAbsent(t *testing.T) {
	bindings := resolveHotkeys(map[string]string{
		"toggle_gemini_yolo": "g",
	})

	if got := bindings[hotkeyToggleYolo]; got != "g" {
		t.Fatalf("toggle_yolo binding = %q, want %q", got, "g")
	}
}

func TestBuildHotkeyLookupRemapAndUnbind(t *testing.T) {
	bindings := resolveHotkeys(map[string]string{
		"delete": "backspace",
		"quit":   "",
	})
	lookup, blocked := buildHotkeyLookup(bindings)

	if got := lookup["backspace"]; got != defaultHotkeyBindings[hotkeyDelete] {
		t.Fatalf("backspace maps to %q, want %q", got, defaultHotkeyBindings[hotkeyDelete])
	}

	if !blocked[defaultHotkeyBindings[hotkeyDelete]] {
		t.Fatalf("default delete key should be blocked when remapped")
	}

	if !blocked["q"] {
		t.Fatalf("q should be blocked when quit is unbound")
	}

	if !blocked["ctrl+c"] {
		t.Fatalf("ctrl+c should be blocked when quit is unbound")
	}
}

func TestHotkeyAliasesShiftAndSymbols(t *testing.T) {
	aliases := hotkeyAliases("shift+f")
	hasUpper := false
	for _, alias := range aliases {
		if alias == "F" {
			hasUpper = true
			break
		}
	}
	if !hasUpper {
		t.Fatalf("shift+f aliases should include F")
	}

	symbolAliases := hotkeyAliases("!")
	hasShiftNum := false
	for _, alias := range symbolAliases {
		if alias == "shift+1" {
			hasShiftNum = true
			break
		}
	}
	if !hasShiftNum {
		t.Fatalf("! aliases should include shift+1")
	}
}

func TestDetachByteFromBinding(t *testing.T) {
	tests := []struct {
		binding string
		want    byte
	}{
		{"ctrl+q", 17},     // 'q' - 'a' + 1 = 17
		{"ctrl+a", 1},      // 'a' - 'a' + 1 = 1
		{"ctrl+z", 26},     // 'z' - 'a' + 1 = 26
		{"ctrl+b", 2},      // 'b' - 'a' + 1 = 2
		{"Ctrl+Q", 17},     // case insensitive
		{"  ctrl+q  ", 17}, // whitespace trimmed
		{"ctrl+\\", 0x1C},
		{"ctrl+]", 0x1D},
		{"ctrl+^", 0x1E},
		{"ctrl+_", 0x1F},
		{"q", 17},       // non-ctrl binding defaults to Ctrl+Q
		{"", 17},        // empty defaults to Ctrl+Q
		{"shift+q", 17}, // non-ctrl prefix defaults to Ctrl+Q
		{"ctrl+1", 17},  // non-letter defaults to Ctrl+Q
	}

	for _, tt := range tests {
		t.Run(tt.binding, func(t *testing.T) {
			if got := DetachByteFromBinding(tt.binding); got != tt.want {
				t.Errorf("DetachByteFromBinding(%q) = %d, want %d", tt.binding, got, tt.want)
			}
		})
	}
}

func TestDetachByteLabel(t *testing.T) {
	tests := []struct {
		b    byte
		want string
	}{
		{17, "Ctrl+Q"},
		{1, "Ctrl+A"},
		{26, "Ctrl+Z"},
		{2, "Ctrl+B"},
		{0x1C, "Ctrl+\\"},
		{0x1D, "Ctrl+]"},
		{0x1E, "Ctrl+^"},
		{0x1F, "Ctrl+_"},
		{0, "Ctrl+Q"},  // out of range defaults
		{27, "Ctrl+Q"}, // ESC byte, not in letter range
	}

	for _, tt := range tests {
		t.Run(tt.want, func(t *testing.T) {
			if got := DetachByteLabel(tt.b); got != tt.want {
				t.Errorf("DetachByteLabel(%d) = %q, want %q", tt.b, got, tt.want)
			}
		})
	}
}

func TestResolvedDetachByte(t *testing.T) {
	// Default (no overrides) should be Ctrl+Q
	if got := ResolvedDetachByte(nil); got != 17 {
		t.Fatalf("ResolvedDetachByte(nil) = %d, want 17", got)
	}

	// Override detach to ctrl+b
	if got := ResolvedDetachByte(map[string]string{"detach": "ctrl+b"}); got != 2 {
		t.Fatalf("ResolvedDetachByte(ctrl+b) = %d, want 2", got)
	}

	// Override detach to ctrl+a
	if got := ResolvedDetachByte(map[string]string{"detach": "ctrl+a"}); got != 1 {
		t.Fatalf("ResolvedDetachByte(ctrl+a) = %d, want 1", got)
	}

	// Unrelated overrides should not affect detach
	if got := ResolvedDetachByte(map[string]string{"quit": "x"}); got != 17 {
		t.Fatalf("ResolvedDetachByte with unrelated override = %d, want 17", got)
	}

	// Unbinding detach (empty string) should default to Ctrl+Q
	if got := ResolvedDetachByte(map[string]string{"detach": ""}); got != 17 {
		t.Fatalf("ResolvedDetachByte with empty override = %d, want 17", got)
	}
}

func TestNormalizeMainKeyWithConfiguredHotkeys(t *testing.T) {
	h := NewHome()
	h.setHotkeys(resolveHotkeys(map[string]string{
		"delete": "backspace",
		"quit":   "",
	}))

	if got := h.normalizeMainKey("backspace"); got != defaultHotkeyBindings[hotkeyDelete] {
		t.Fatalf("backspace normalized to %q, want %q", got, defaultHotkeyBindings[hotkeyDelete])
	}

	if got := h.normalizeMainKey(defaultHotkeyBindings[hotkeyDelete]); got != "" {
		t.Fatalf("default delete key should be blocked after remap, got %q", got)
	}

	if got := h.normalizeMainKey("ctrl+c"); got != "" {
		t.Fatalf("ctrl+c should be blocked when quit is unbound, got %q", got)
	}
}
