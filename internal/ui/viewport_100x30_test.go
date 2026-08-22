package ui

import (
	"fmt"
	"strings"
	"testing"

	"github.com/asheshgoplani/agent-deck/internal/session"
	"github.com/charmbracelet/lipgloss"
)

func TestViewportDialogContentPinsChromeAndFocusedField(t *testing.T) {
	var body strings.Builder
	body.WriteString("New Session\n  in group: default\n")
	for i := 0; i < 32; i++ {
		prefix := "  "
		if i == 21 {
			prefix = "▶ "
		}
		fmt.Fprintf(&body, "%sfield %02d\n", prefix, i)
	}
	body.WriteString("Tab next │ Enter create │ Esc cancel")

	got := viewportDialogContent(body.String(), 76, 24, 23)
	plain := stripAnsi(got)
	for _, pin := range []string{"New Session", "▶ field 21", "Enter create", "↑ more fields"} {
		if !strings.Contains(plain, pin) {
			t.Fatalf("viewport lost pinned %q:\n%s", pin, plain)
		}
	}
	if height := lipgloss.Height(got); height > 24 {
		t.Fatalf("content height = %d, want <= 24 (30 rows minus dialog chrome)", height)
	}
}

func TestViewportDialogContentPinsEntireWrappedFooter(t *testing.T) {
	var body strings.Builder
	body.WriteString("New Session\n  in group: default\n")
	for i := 0; i < 32; i++ {
		prefix := "  "
		if i == 21 {
			prefix = "▶ "
		}
		fmt.Fprintf(&body, "%sfield %02d\n", prefix, i)
	}
	body.WriteString("Tab next │ Shift+Tab previous │ ^S create │ Esc cancel")

	got := viewportDialogContent(body.String(), 40, 24, 23)
	plain := stripAnsi(got)
	for _, pin := range []string{"New Session", "▶ field 21", "^S", "create", "Esc cancel"} {
		if !strings.Contains(plain, pin) {
			t.Fatalf("viewport lost pinned %q from wrapped footer:\n%s", pin, plain)
		}
	}
	if height := lipgloss.Height(got); height > 24 {
		t.Fatalf("content height = %d, want <= 24", height)
	}
}

func TestViewportDialogContentDoesNotChangeTallLayout(t *testing.T) {
	const content = "New Session\n  in group: default\n\n▶ Name:\n  demo\n\nEnter create"
	if got := viewportDialogContent(content, 76, 42, 3); got != content {
		t.Fatalf("160x48 content changed:\n got %q\nwant %q", got, content)
	}
}

func TestViewportDialogContentUsesHeaderHeightAndFocusedRowIdentity(t *testing.T) {
	for _, tc := range []struct {
		name        string
		width       int
		height      int
		contextLine string
	}{
		{name: "100x30", width: 76, height: 24, contextLine: "  in group: " + strings.Repeat("context-", 11)},
		{name: "160x48", width: 136, height: 42, contextLine: "  in group: " + strings.Repeat("context-", 11)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			logical := []string{"New Session", tc.contextLine, "", "  ▶ repeated picker glyph"}
			for i := 0; i < 60; i++ {
				logical = append(logical, fmt.Sprintf("  field %02d", i))
			}
			focusLine := len(logical)
			logical = append(logical, "▶ selected row identity", "  selected value", "Enter create │ Esc cancel")
			got := viewportDialogContent(strings.Join(logical, "\n"), tc.width, tc.height, focusLine)
			plain := stripAnsi(got)
			for _, pin := range []string{"New Session", "in group:", "selected row", "Enter create"} {
				if !strings.Contains(plain, pin) {
					t.Fatalf("%s viewport lost %q:\n%s", tc.name, pin, plain)
				}
			}
			if strings.Contains(plain, "repeated picker glyph") {
				t.Fatalf("%s anchored the first repeated glyph instead of selected row identity:\n%s", tc.name, plain)
			}
			if gotContexts := strings.Count(plain, "context-"); gotContexts != 11 {
				t.Fatalf("%s retained %d/11 wrapped group-context segments:\n%s", tc.name, gotContexts, plain)
			}
			if gotHeight := lipgloss.Height(got); gotHeight > tc.height {
				t.Fatalf("%s content height = %d, want <= %d", tc.name, gotHeight, tc.height)
			}
		})
	}
}

func TestNewDialogFits100x30AtModelFocus(t *testing.T) {
	d := NewNewDialog()
	d.SetDefaultTool("codex")
	d.SetSize(100, 30)
	d.Show()
	d.focusIndex = d.indexOf(focusModel)
	d.updateFocus()

	view := d.View()
	plain := stripAnsi(view)
	for _, pin := range []string{"New Session", "▶ Model ID:", "select", "create"} {
		if !strings.Contains(plain, pin) {
			t.Fatalf("100x30 model view lost %q:\n%s", pin, plain)
		}
	}
	if got := lipgloss.Height(view); got > 30 {
		t.Fatalf("100x30 dialog rendered %d rows", got)
	}
}

func TestNewDialogFits100x30AtIndentedClaudeOptionFocus(t *testing.T) {
	for _, tc := range []struct {
		name       string
		focusIndex int
		want       string
	}{
		{name: "extra args", focusIndex: 5, want: "▶ Extra args:"},
		{name: "start query", focusIndex: 6, want: "▶ Start query:"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			d := NewNewDialog()
			d.SetDefaultTool("claude")
			d.SetSize(100, 30)
			d.Show()
			d.focusIndex = d.indexOf(focusOptions)
			d.updateFocus()
			d.claudeOptions.focusIndex = tc.focusIndex
			d.claudeOptions.updateInputFocus()

			view := d.View()
			plain := stripAnsi(view)
			for _, pin := range []string{"New Session", tc.want, "create"} {
				if !strings.Contains(plain, pin) {
					t.Fatalf("100x30 Claude-options view lost %q:\n%s", pin, plain)
				}
			}
			if got := lipgloss.Height(view); got > 30 {
				t.Fatalf("100x30 dialog rendered %d rows", got)
			}
		})
	}
}

func TestClaudeOptionsFocusedLineTracksConditionalRows(t *testing.T) {
	p := NewClaudeOptionsPanel()
	p.Focus()
	p.sessionMode = 2
	p.skipPermissions = true
	p.autoMode = true
	p.focusIndex = 7 // Start query, after the conditional resume and warning rows.
	if got, want := p.FocusedLine(), 9; got != want {
		t.Fatalf("Start query logical line = %d, want %d", got, want)
	}
	lines := strings.Split(stripAnsi(p.View()), "\n")
	if got := lines[p.FocusedLine()]; !strings.Contains(got, "Start query:") {
		t.Fatalf("focused identity resolved to %q, want Start query row", got)
	}
}

func TestNewDialogWideLayoutRetainsAllFields(t *testing.T) {
	d := NewNewDialog()
	d.SetDefaultTool("codex")
	d.SetSize(160, 48)
	d.Show()
	plain := stripAnsi(d.View())
	for _, field := range []string{"New Session", "Name:", "Command:", "Model ID:", "Path:", "Create in worktree", "Run in Docker sandbox", "Multi-repo mode", "create"} {
		if !strings.Contains(plain, field) {
			t.Fatalf("160x48 layout lost %q:\n%s", field, plain)
		}
	}
}

func TestGeminiModelVisibleRowsMutationPins(t *testing.T) {
	if got := geminiModelVisibleRows(30); got != 15 {
		t.Fatalf("100x30 visible rows = %d, want 15", got)
	}
	if got := geminiModelVisibleRows(48); got != 15 {
		t.Fatalf("160x48 visible rows = %d, want 15", got)
	}
	if got := geminiModelVisibleRows(14); got != 3 {
		t.Fatalf("minimum visible rows = %d, want 3", got)
	}
}

func TestGeminiModelPickerFits100x30AndPinsSelection(t *testing.T) {
	d := NewGeminiModelDialog()
	d.visible = true
	d.SetSize(100, 30)
	for i := 0; i < 24; i++ {
		d.models = append(d.models, fmt.Sprintf("gemini-model-%02d", i))
	}
	d.cursor = 20

	view := d.View()
	plain := stripAnsi(view)
	for _, pin := range []string{"Select Gemini Model", "gemini-model-20", "Enter Select", "↑ more models", "↓ more models"} {
		if !strings.Contains(plain, pin) {
			t.Fatalf("100x30 picker lost %q:\n%s", pin, plain)
		}
	}
	if got := lipgloss.Height(view); got > 30 {
		t.Fatalf("100x30 picker rendered %d rows", got)
	}
}

func TestFullFooterUsesCompactReadableTierAt100Columns(t *testing.T) {
	h := NewHome()
	t.Cleanup(func() { h.cancel() })
	h.width = 100
	h.flatItems = []session.Item{{
		Type:    session.ItemTypeSession,
		Session: &session.Instance{ID: "skills-footer", Tool: "claude", Title: "Skills footer"},
	}}
	h.cursor = 0

	footer := h.renderHelpBarWidthAdaptive()
	compact := h.renderHelpBarCompact()
	full := h.renderHelpBarFull()
	if footer != compact {
		t.Fatalf("100-column footer did not use compact tier:\n%s", stripAnsi(footer))
	}
	if footer == full {
		t.Fatal("test fixture does not distinguish compact and full footer tiers")
	}
	plain := stripAnsi(footer)
	if !strings.Contains(plain, "Skills") {
		t.Fatalf("100-column selected-session footer lost Skills:\n%s", plain)
	}
	for _, line := range strings.Split(footer, "\n") {
		if got := lipgloss.Width(line); got > 100 {
			t.Fatalf("footer line width = %d, want <= 100", got)
		}
	}
}
