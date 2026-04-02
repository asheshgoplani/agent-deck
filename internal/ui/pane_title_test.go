package ui

import (
	"strings"
	"testing"

	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"
)

func TestCleanPaneTitle(t *testing.T) {
	tests := []struct {
		name  string
		input string
		want  string
	}{
		{"empty", "", ""},
		{"generic claude", "✳ Claude Code", ""},
		{"generic gemini", "✳ Gemini CLI", ""},
		{"generic codex", "✳ Codex CLI", ""},
		{"braille spinner with task", "⠐ Fix the KPIs (Branch)", "Fix the KPIs (Branch)"},
		{"done marker with task", "✳ Run and verify session tests", "Run and verify session tests"},
		{"multiple markers", "✳✻ Some task", "Some task"},
		{"just markers", "✳✻✽", ""},
		{"no markers", "Hello world", "Hello world"},
		{"hostname only", "29fa91017da8", "29fa91017da8"},
		{"braille only", "⠐ Claude Code", ""},
		{"whitespace after strip", "✳  Spaced task ", "Spaced task"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := cleanPaneTitle(tt.input)
			if got != tt.want {
				t.Errorf("cleanPaneTitle(%q) = %q, want %q", tt.input, got, tt.want)
			}
		})
	}
}

// TestPaneTitleDynamicWidth verifies pane title fills available space without line breaks.
func TestPaneTitleDynamicWidth(t *testing.T) {
	// Mirrors the post-Sprintf logic in renderSessionItem.
	renderPaneTitle := func(baseRowWidth, termWidth int, paneTitle string) string {
		remaining := termWidth - baseRowWidth - 2
		if remaining <= 10 {
			return ""
		}
		pt := paneTitle
		if lipgloss.Width(pt) > remaining {
			pt = ansi.Truncate(pt, remaining, "…")
		}
		return DimStyle.Render(" " + pt)
	}

	t.Run("no line breaks in output", func(t *testing.T) {
		result := renderPaneTitle(30, 120, "Fix the KPIs and run the full regression suite for all modules")
		if strings.Contains(result, "\n") {
			t.Error("pane title output contains newline")
		}
	})

	t.Run("omitted when no space", func(t *testing.T) {
		result := renderPaneTitle(78, 80, "Some task")
		if result != "" {
			t.Errorf("expected empty when no space, got %q", result)
		}
	})

	t.Run("omitted when remaining too small", func(t *testing.T) {
		result := renderPaneTitle(70, 80, "Some task")
		if result != "" {
			t.Errorf("expected empty when remaining < 10, got %q", result)
		}
	})

	t.Run("shown when enough space", func(t *testing.T) {
		result := renderPaneTitle(40, 120, "Fix the KPIs")
		if result == "" {
			t.Error("expected pane title to be shown with enough space")
		}
		if !strings.Contains(result, "Fix the KPIs") {
			t.Errorf("expected result to contain pane title text, got %q", result)
		}
	})

	t.Run("truncated for narrow terminal", func(t *testing.T) {
		longTitle := "Implement the full authentication system with OAuth2"
		result := renderPaneTitle(40, 60, longTitle)
		if result == "" {
			t.Error("expected pane title to be shown")
		}
		if strings.Contains(result, "OAuth2") {
			t.Errorf("expected title to be truncated, but end is present: %q", result)
		}
	})

	t.Run("wide terminal shows full title", func(t *testing.T) {
		title := "Fix the KPIs and run tests"
		result := renderPaneTitle(40, 200, title)
		if !strings.Contains(result, title) {
			t.Errorf("expected full title on wide terminal, got %q", result)
		}
	})

	t.Run("deeply nested row leaves less space", func(t *testing.T) {
		title := "A very long task description that should be truncated on nested items"
		result := renderPaneTitle(70, 100, title)
		if result == "" {
			t.Error("expected pane title even for nested items")
		}
		if strings.Contains(result, "nested items") {
			t.Errorf("expected truncation for nested row, got %q", result)
		}
	})
}
