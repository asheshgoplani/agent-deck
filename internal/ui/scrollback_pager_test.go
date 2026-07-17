package ui

import (
	"strings"
	"testing"
)

// TestResolvedScrollbackTrigger covers the [hotkeys].scrollback resolution:
// default PageUp, a ctrl+letter chord, explicit disable, and the
// unrecognized-value fallback.
func TestResolvedScrollbackTrigger(t *testing.T) {
	tests := []struct {
		name         string
		overrides    map[string]string
		wantPageUp   bool
		wantKeyByte  byte
		wantEnabled  bool
		wantLabelPfx string
	}{
		{
			name:         "default is pageup",
			overrides:    nil,
			wantPageUp:   true,
			wantEnabled:  true,
			wantLabelPfx: "PageUp",
		},
		{
			name:         "explicit pageup",
			overrides:    map[string]string{"scrollback": "pageup"},
			wantPageUp:   true,
			wantEnabled:  true,
			wantLabelPfx: "PageUp",
		},
		{
			name:         "ctrl+g chord",
			overrides:    map[string]string{"scrollback": "ctrl+g"},
			wantKeyByte:  7,
			wantEnabled:  true,
			wantLabelPfx: "Ctrl+G",
		},
		{
			name:        "empty string disables",
			overrides:   map[string]string{"scrollback": ""},
			wantEnabled: false,
		},
		{
			name:         "unrecognized value falls back to pageup",
			overrides:    map[string]string{"scrollback": "banana"},
			wantPageUp:   true,
			wantEnabled:  true,
			wantLabelPfx: "PageUp",
		},
		{
			name:         "action name case-insensitive",
			overrides:    map[string]string{"SCROLLBACK": "ctrl+g"},
			wantKeyByte:  7,
			wantEnabled:  true,
			wantLabelPfx: "Ctrl+G",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := ResolvedScrollbackTrigger(tt.overrides)
			if got.OnPageUp != tt.wantPageUp {
				t.Errorf("OnPageUp = %v, want %v", got.OnPageUp, tt.wantPageUp)
			}
			if got.KeyByte != tt.wantKeyByte {
				t.Errorf("KeyByte = %d, want %d", got.KeyByte, tt.wantKeyByte)
			}
			if got.Enabled() != tt.wantEnabled {
				t.Errorf("Enabled() = %v, want %v", got.Enabled(), tt.wantEnabled)
			}
			if tt.wantLabelPfx != "" && got.Label() != tt.wantLabelPfx {
				t.Errorf("Label() = %q, want %q", got.Label(), tt.wantLabelPfx)
			}
			if !tt.wantEnabled && got.Label() != "" {
				t.Errorf("disabled Label() = %q, want empty", got.Label())
			}
		})
	}
}

// TestScrollbackPager_LoadingAndContent verifies the pager opens in a loading
// state, then installs content pinned to the live end (bottom).
func TestScrollbackPager_LoadingAndContent(t *testing.T) {
	p := NewScrollbackPager()
	if p.IsVisible() {
		t.Fatal("new pager should be hidden")
	}
	p.Show("mysession", "id-1", 80, 10)
	if !p.IsVisible() {
		t.Fatal("Show should make the pager visible")
	}
	if p.SessionID() != "id-1" {
		t.Fatalf("SessionID = %q, want id-1", p.SessionID())
	}
	if !p.loading {
		t.Fatal("pager should start in loading state")
	}
	if !strings.Contains(p.View(), "loading") {
		t.Errorf("loading View should mention loading, got:\n%s", p.View())
	}

	// 20 lines into a body of 8 (height 10 - 2 chrome) => bottom offset = 12.
	var lines []string
	for i := 1; i <= 20; i++ {
		lines = append(lines, "line-"+string(rune('A'+i-1)))
	}
	p.SetContent(strings.Join(lines, "\n"))
	if p.loading {
		t.Error("SetContent should clear loading")
	}
	if got := p.maxOffset(); p.offset != got {
		t.Errorf("SetContent should pin to bottom: offset=%d, maxOffset=%d", p.offset, got)
	}
	// Body height 8 => maxOffset 12.
	if p.maxOffset() != 12 {
		t.Errorf("maxOffset = %d, want 12", p.maxOffset())
	}
}

// TestScrollbackPager_ScrollAndClamp verifies scrolling stays within bounds and
// Top/Bottom jump to the extremes.
func TestScrollbackPager_ScrollAndClamp(t *testing.T) {
	p := NewScrollbackPager()
	p.Show("s", "id", 80, 10) // body height 8
	lines := make([]string, 20)
	for i := range lines {
		lines[i] = "row"
	}
	p.SetContent(strings.Join(lines, "\n"))

	p.Top()
	if p.offset != 0 {
		t.Errorf("Top offset = %d, want 0", p.offset)
	}
	// Cannot scroll above the top.
	p.ScrollUp(5)
	if p.offset != 0 {
		t.Errorf("ScrollUp past top: offset = %d, want 0", p.offset)
	}
	p.ScrollDown(3)
	if p.offset != 3 {
		t.Errorf("ScrollDown 3: offset = %d, want 3", p.offset)
	}
	p.Bottom()
	if p.offset != 12 {
		t.Errorf("Bottom offset = %d, want 12", p.offset)
	}
	// Cannot scroll below the bottom.
	p.ScrollDown(100)
	if p.offset != 12 {
		t.Errorf("ScrollDown past bottom: offset = %d, want 12", p.offset)
	}
	// PageUp scrolls by body-1 (7) with overlap.
	p.PageUp()
	if p.offset != 5 {
		t.Errorf("PageUp offset = %d, want 5", p.offset)
	}
	p.PageDown()
	if p.offset != 12 {
		t.Errorf("PageDown offset = %d, want 12", p.offset)
	}
}

// TestScrollbackPager_ShortBuffer verifies a buffer that fits entirely on
// screen never scrolls (maxOffset 0).
func TestScrollbackPager_ShortBuffer(t *testing.T) {
	p := NewScrollbackPager()
	p.Show("s", "id", 80, 20) // body height 18
	p.SetContent("only\ntwo lines")
	if p.maxOffset() != 0 {
		t.Errorf("short buffer maxOffset = %d, want 0", p.maxOffset())
	}
	p.ScrollUp(5)
	if p.offset != 0 {
		t.Errorf("short buffer offset after ScrollUp = %d, want 0", p.offset)
	}
}

// TestScrollbackPager_TrailingBlankTrimmed verifies capture-pane's common
// trailing blank line is trimmed so the initial view isn't blank.
func TestScrollbackPager_TrailingBlankTrimmed(t *testing.T) {
	p := NewScrollbackPager()
	p.Show("s", "id", 80, 10)
	p.SetContent("a\nb\nc\n\n")
	if len(p.lines) != 3 {
		t.Errorf("trailing blanks not trimmed: got %d lines %q", len(p.lines), p.lines)
	}
}

// TestScrollbackPager_ViewStates verifies the body renders each state and that
// content lines get an SGR reset appended.
func TestScrollbackPager_ViewStates(t *testing.T) {
	p := NewScrollbackPager()

	// Error state (width 80 so the message isn't truncated).
	p.Show("s", "id", 80, 8)
	p.SetError("capture timed out")
	if !strings.Contains(p.View(), "capture timed out") {
		t.Errorf("error View missing message:\n%s", p.View())
	}

	// Empty state.
	p.Show("s", "id", 80, 8)
	p.SetContent("")
	if !strings.Contains(p.View(), "No scrollback") {
		t.Errorf("empty View missing message:\n%s", p.View())
	}

	// Content state.
	p.Show("s2", "id2", 80, 8)
	p.SetContent("hello\nworld")
	v := p.View()
	if !strings.Contains(v, "hello") || !strings.Contains(v, "world") {
		t.Errorf("content View missing lines:\n%s", v)
	}
	if !strings.Contains(v, "\x1b[0m") {
		t.Error("content lines should get an SGR reset")
	}
	if !strings.Contains(v, "s2") {
		t.Error("header should include the session title")
	}
	// Footer hint present.
	if !strings.Contains(v, "start") || !strings.Contains(v, "session") {
		t.Errorf("footer hints missing:\n%s", v)
	}
}

// TestScrollbackPager_Hide clears state.
func TestScrollbackPager_Hide(t *testing.T) {
	p := NewScrollbackPager()
	p.Show("s", "id", 40, 8)
	p.SetContent("a\nb\nc")
	p.Hide()
	if p.IsVisible() || p.SessionID() != "" || len(p.lines) != 0 {
		t.Errorf("Hide did not fully reset: visible=%v id=%q lines=%d",
			p.IsVisible(), p.SessionID(), len(p.lines))
	}
	if p.View() != "" {
		t.Error("hidden View should be empty")
	}
}

// TestScrollbackPager_ResizeReclamps verifies a resize re-clamps the offset so
// it can't point past the end after the body grows.
func TestScrollbackPager_ResizeReclamps(t *testing.T) {
	p := NewScrollbackPager()
	p.Show("s", "id", 40, 10) // body 8
	lines := make([]string, 20)
	for i := range lines {
		lines[i] = "x"
	}
	p.SetContent(strings.Join(lines, "\n")) // offset pinned to 12
	// Grow the terminal so the body height jumps to 30 (all lines fit).
	p.SetSize(40, 32)
	if p.offset != 0 {
		t.Errorf("after grow, offset = %d, want 0 (all lines fit)", p.offset)
	}
}
