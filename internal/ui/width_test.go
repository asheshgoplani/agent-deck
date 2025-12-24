package ui_test

import (
	"fmt"
	"strings"
	"testing"

	"github.com/asheshgoplani/agent-deck/internal/session"
	"github.com/asheshgoplani/agent-deck/internal/tmux"
	"github.com/asheshgoplani/agent-deck/internal/ui"
	"github.com/mattn/go-runewidth"
)

// stripANSIForTest removes ANSI escape codes for accurate width measurement
func stripANSIForTest(s string) string {
	return tmux.StripANSI(s)
}

// measureVisualWidth measures the visual width of a string (strips ANSI, measures display width)
func measureVisualWidth(s string) int {
	clean := stripANSIForTest(s)
	return runewidth.StringWidth(clean)
}

// assertMaxWidth verifies that all lines in output respect maxWidth
func assertMaxWidth(t *testing.T, output string, maxWidth int, context string) {
	t.Helper()
	lines := strings.Split(output, "\n")
	for i, line := range lines {
		width := measureVisualWidth(line)
		if width > maxWidth {
			t.Errorf("%s: line %d exceeds maxWidth %d (actual: %d)\nLine: %q\nClean: %q",
				context, i, maxWidth, width, line, stripANSIForTest(line))
		}
	}
}

// assertExactHeight verifies output has exactly the expected number of lines
func assertExactHeight(t *testing.T, output string, expectedLines int, context string) {
	t.Helper()
	lines := strings.Split(output, "\n")
	if len(lines) != expectedLines {
		t.Errorf("%s: expected %d lines, got %d", context, expectedLines, len(lines))
	}
}

func TestWidthTestHelpers(t *testing.T) {
	t.Run("ANSI stripping", func(t *testing.T) {
		tests := []struct {
			name  string
			input string
			want  string
		}{
			{
				name:  "simple color code",
				input: "\x1b[31mHello\x1b[0m",
				want:  "Hello",
			},
			{
				name:  "multiple color codes",
				input: "\x1b[32mGreen\x1b[0m \x1b[34mBlue\x1b[0m",
				want:  "Green Blue",
			},
			{
				name:  "no ANSI codes",
				input: "Plain text",
				want:  "Plain text",
			},
			{
				name:  "complex styling",
				input: "\x1b[1m\x1b[33mBold Yellow\x1b[0m",
				want:  "Bold Yellow",
			},
		}

		for _, tt := range tests {
			t.Run(tt.name, func(t *testing.T) {
				got := stripANSIForTest(tt.input)
				if got != tt.want {
					t.Errorf("stripANSIForTest() = %q, want %q", got, tt.want)
				}
			})
		}
	})

	t.Run("width measurement", func(t *testing.T) {
		tests := []struct {
			name      string
			input     string
			wantWidth int
		}{
			{
				name:      "ASCII with ANSI",
				input:     "\x1b[31mHello\x1b[0m",
				wantWidth: 5,
			},
			{
				name:      "plain ASCII",
				input:     "Hello World",
				wantWidth: 11,
			},
			{
				name:      "empty string",
				input:     "",
				wantWidth: 0,
			},
			{
				name:      "wide characters",
				input:     "日本語",
				wantWidth: 6, // Each character is width 2
			},
			{
				name:      "mixed ASCII and wide",
				input:     "Hello 世界",
				wantWidth: 10, // "Hello " = 6, "世界" = 4
			},
		}

		for _, tt := range tests {
			t.Run(tt.name, func(t *testing.T) {
				got := measureVisualWidth(tt.input)
				if got != tt.wantWidth {
					t.Errorf("measureVisualWidth() = %d, want %d", got, tt.wantWidth)
				}
			})
		}
	})

	t.Run("assertMaxWidth", func(t *testing.T) {
		// Test that it passes when all lines are within limit
		validOutput := "Short\nLines\nHere"
		assertMaxWidth(t, validOutput, 10, "valid output")

		// Test that it would fail for too-wide lines (we can't directly test t.Errorf)
		// This is just documenting the helper exists and compiles correctly
	})

	t.Run("assertExactHeight", func(t *testing.T) {
		// Test that it passes when line count matches
		output := "Line 1\nLine 2\nLine 3"
		assertExactHeight(t, output, 3, "three lines")

		// Test that it would fail for wrong count (we can't directly test t.Errorf)
		// This is just documenting the helper exists and compiles correctly
	})
}

func TestRenderPreviewPane_RespectsMaxWidth(t *testing.T) {
	// Create a minimal Home instance with test data
	h := ui.NewTestHome()
	h.SetFlatItemsForTest([]session.Item{
		{
			Type: session.ItemTypeSession,
			Session: &session.Instance{
				ID:              "test-123",
				Title:           "Test Session with a Very Long Title That Should Be Truncated",
				ProjectPath:     "/very/long/path/that/exceeds/width/limit/and/should/be/truncated",
				GroupPath:       "test-group",
				Tool:            "claude",
				Status:          session.StatusRunning,
				ClaudeSessionID: "session-abc-123",
			},
		},
	})
	h.SetCursorForTest(0)

	// Test various widths
	testCases := []struct {
		width  int
		height int
	}{
		{width: 50, height: 20},
		{width: 80, height: 30},
		{width: 120, height: 40},
	}

	for _, tc := range testCases {
		t.Run(fmt.Sprintf("width=%d", tc.width), func(t *testing.T) {
			output := h.RenderPreviewPaneForTest(tc.width, tc.height)

			// CRITICAL: Every line must respect maxWidth
			assertMaxWidth(t, output, tc.width, "renderPreviewPane")

			// Should have exactly the requested height
			assertExactHeight(t, output, tc.height, "renderPreviewPane")
		})
	}
}

func TestRenderSessionList_RespectsMaxWidth(t *testing.T) {
	h := ui.NewTestHome()
	h.SetFlatItemsForTest([]session.Item{
		{
			Type: session.ItemTypeGroup,
			Group: &session.Group{
				Name:     "Test Group with a Very Long Name That Should Be Truncated",
				Path:     "long-group",
				Expanded: true,
			},
			Level: 0,
		},
		{
			Type: session.ItemTypeSession,
			Session: &session.Instance{
				ID:          "sess-1",
				Title:       "Session with an Extremely Long Title That Should Be Truncated",
				Tool:        "claude",
				Status:      session.StatusRunning,
				GroupPath:   "long-group",
			},
			Level: 1,
		},
	})
	h.SetCursorForTest(0)

	testCases := []struct {
		width  int
		height int
	}{
		{width: 30, height: 10},
		{width: 50, height: 20},
		{width: 80, height: 30},
	}

	for _, tc := range testCases {
		t.Run(fmt.Sprintf("width=%d", tc.width), func(t *testing.T) {
			output := h.RenderSessionListForTest(tc.width, tc.height)

			assertMaxWidth(t, output, tc.width, "renderSessionList")
			assertExactHeight(t, output, tc.height, "renderSessionList")
		})
	}
}

func TestView_PanelWidthConstraints(t *testing.T) {
	// Create Home with realistic data
	h := ui.NewTestHome()
	h.SetFlatItemsForTest([]session.Item{
		{
			Type: session.ItemTypeSession,
			Session: &session.Instance{
				ID:          "test-1",
				Title:       "Session with Very Long Title",
				ProjectPath: "/path/to/project",
				Tool:        "claude",
				Status:      session.StatusRunning,
			},
		},
	})
	h.SetCursorForTest(0)
	h.SetSizeForTest(120, 30)

	// Render full view
	output := h.View()

	// Debug: show actual line count and any width violations
	lines := strings.Split(output, "\n")
	t.Logf("Actual line count: %d (expected 30)", len(lines))

	// Each line of the entire view must not exceed terminal width
	assertMaxWidth(t, output, 120, "Full View")

	// View should have exactly h.height lines (accounting for ensureExactHeight)
	assertExactHeight(t, output, 30, "Full View")
}

func TestRenderPanelTitle_RespectsWidth(t *testing.T) {
	h := ui.NewTestHome()

	testCases := []struct {
		title string
		width int
	}{
		{title: "SESSIONS", width: 30},
		{title: "PREVIEW", width: 50},
		{title: "VERY_LONG_TITLE_THAT_EXCEEDS_WIDTH", width: 20},
		{title: "SHORT", width: 100},
	}

	for _, tc := range testCases {
		t.Run(fmt.Sprintf("%s_width=%d", tc.title, tc.width), func(t *testing.T) {
			output := h.RenderPanelTitleForTest(tc.title, tc.width)

			// Panel title is 2 lines (title + underline)
			assertExactHeight(t, output, 2, "renderPanelTitle")

			// CRITICAL: Every line must respect maxWidth
			assertMaxWidth(t, output, tc.width, "renderPanelTitle")

			// Verify structure: should be title\nunderline
			lines := strings.Split(output, "\n")
			if len(lines) != 2 {
				t.Fatalf("Expected 2 lines, got %d", len(lines))
			}

			// Check that title line exists and has content
			titleLine := stripANSIForTest(lines[0])
			if titleLine == "" {
				t.Error("Title line is empty")
			}

			// Check that underline exists and uses dash character
			underline := stripANSIForTest(lines[1])
			if !strings.Contains(underline, "─") {
				t.Errorf("Underline should contain '─' character, got: %q", underline)
			}
		})
	}
}
