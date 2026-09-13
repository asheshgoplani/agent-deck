package ui

import (
	"testing"

	"github.com/asheshgoplani/agent-deck/internal/session"
	"github.com/charmbracelet/lipgloss"
)

// TestToolIconColor_RegistryMatchesLegacySwitch pins the exact icon and color
// the former hardcoded switches in styles.go returned for every built-in tool.
// ToolIcon/ToolColor now read the tool registry first (issue #2136); this test
// guards that the registry data reproduces the legacy output byte for byte.
// The expected values are the literal arms of the old switches, not derived
// from the registry, so drift in either direction fails here.
func TestToolIconColor_RegistryMatchesLegacySwitch(t *testing.T) {
	InitTheme("dark")

	legacy := map[string]struct {
		icon  string
		color lipgloss.Color
	}{
		"claude":   {"🤖", ColorOrange},
		"gemini":   {"✨", ColorPurple},
		"opencode": {"🌐", ColorTextDim},
		"codex":    {"💻", ColorCyan},
		"pi":       {"π", ColorAccent},
		"copilot":  {"🐙", ColorAccent},
		"crush":    {"💘", ColorPurple},
		"cursor":   {"📝", ColorAccent},
		"hermes":   {"☤", ColorYellow},
		"deepseek": {"🐋", ColorCyan},
		"aider":    {"🐚", ColorRed},
		"shell":    {"🐚", ColorTextDim},
	}

	builtins := session.Init(nil).All()
	if len(builtins) != len(legacy) {
		t.Fatalf("registry has %d built-ins, legacy table has %d; add the new tool to this table", len(builtins), len(legacy))
	}
	for _, def := range builtins {
		name := def.Command
		want, ok := legacy[name]
		if !ok {
			t.Errorf("built-in %q has no legacy expectation in this table", name)
			continue
		}
		if got := ToolIcon(name); got != want.icon {
			t.Errorf("ToolIcon(%q) = %q, want %q", name, got, want.icon)
		}
		if got := ToolColor(name); got != want.color {
			t.Errorf("ToolColor(%q) = %q, want %q", name, got, want.color)
		}
	}
}

// TestToolIconColor_UnknownFallsBackToSwitch keeps the legacy default arms for
// names the registry does not know.
func TestToolIconColor_UnknownFallsBackToSwitch(t *testing.T) {
	InitTheme("dark")
	if got := ToolIcon("no-such-tool"); got != IconShell {
		t.Errorf("ToolIcon(unknown) = %q, want %q", got, IconShell)
	}
	if got := ToolColor("no-such-tool"); got != ColorTextDim {
		t.Errorf("ToolColor(unknown) = %q, want %q", got, ColorTextDim)
	}
}

// TestPaletteColor covers the slot-name resolution and the verbatim passthrough
// used for custom [tools.<name>] color values.
func TestPaletteColor(t *testing.T) {
	InitTheme("dark")
	cases := []struct {
		in   string
		want lipgloss.Color
		ok   bool
	}{
		{"", "", false},
		{"orange", ColorOrange, true},
		{"purple", ColorPurple, true},
		{"cyan", ColorCyan, true},
		{"accent", ColorAccent, true},
		{"yellow", ColorYellow, true},
		{"red", ColorRed, true},
		{"#ff00ff", lipgloss.Color("#ff00ff"), true},
		{"208", lipgloss.Color("208"), true},
	}
	for _, tc := range cases {
		got, ok := paletteColor(tc.in)
		if got != tc.want || ok != tc.ok {
			t.Errorf("paletteColor(%q) = (%q, %v), want (%q, %v)", tc.in, got, ok, tc.want, tc.ok)
		}
	}
}
