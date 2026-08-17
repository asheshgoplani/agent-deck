package ui

import (
	"fmt"
	"strings"
	"testing"

	"github.com/asheshgoplani/agent-deck/internal/session"
	tea "github.com/charmbracelet/bubbletea"
)

// previewScrollSessionWithLines returns a session instance whose preview cache
// is seeded with N numbered lines ("line-0"..."line-(N-1)"), and a *Home
// configured for dual layout so mouse-wheel routing has a preview region.
func previewScrollSessionWithLines(t *testing.T, width, height, numLines int) (*Home, *session.Instance) {
	t.Helper()
	inst := session.NewInstance("scroll-target", t.TempDir())
	inst.Status = session.StatusRunning

	lines := make([]string, numLines)
	for i := range lines {
		lines[i] = fmt.Sprintf("line-%d", i)
	}
	content := strings.Join(lines, "\n")

	h := NewHome()
	h.width = width
	h.height = height
	h.initialLoading = false

	h.instancesMu.Lock()
	h.instances = []*session.Instance{inst}
	h.instanceByID[inst.ID] = inst
	h.instancesMu.Unlock()

	h.flatItems = []session.Item{
		{Type: session.ItemTypeSession, Session: inst},
	}
	h.cursor = 0
	h.lastClickIndex = -1
	h.setHotkeys(resolveHotkeys(nil))

	h.previewCacheMu.Lock()
	h.previewCache[inst.ID] = content
	h.previewCacheMu.Unlock()

	return h, inst
}

// Test 1: Mouse wheel over the preview pane region (dual layout) increments
// previewScrollOffset and does NOT move the session list cursor.
func TestPreviewScroll_MouseWheel_OverPreviewPane_IncrementsOffset(t *testing.T) {
	h, _ := previewScrollSessionWithLines(t, 120, 40, 50)

	// width=120, dual layout threshold is >=80, leftWidth = int(120*0.35) = 42.
	// X=100 is well inside the preview region.
	msg := tea.MouseMsg{X: 100, Y: 10, Button: tea.MouseButtonWheelUp}
	model, _ := h.Update(msg)
	h = model.(*Home)

	if h.previewScrollOffset != 1 {
		t.Fatalf("WheelUp over preview: previewScrollOffset=%d, want 1", h.previewScrollOffset)
	}
	if h.cursor != 0 {
		t.Fatalf("WheelUp over preview: cursor=%d, want 0 (cursor should not move)", h.cursor)
	}
}

// Test 2: Mouse wheel over the list region (dual layout) moves the session
// list cursor and resets any preview scroll offset.
func TestPreviewScroll_MouseWheel_OverList_MovesCursor_ResetsOffset(t *testing.T) {
	h, _ := previewScrollSessionWithLines(t, 120, 40, 50)

	// Add a second session so there's somewhere to move the cursor to.
	inst2 := session.NewInstance("other", t.TempDir())
	inst2.Status = session.StatusRunning
	h.instancesMu.Lock()
	h.instances = append(h.instances, inst2)
	h.instanceByID[inst2.ID] = inst2
	h.instancesMu.Unlock()
	h.flatItems = append(h.flatItems, session.Item{Type: session.ItemTypeSession, Session: inst2})

	// Pre-seed a non-zero preview offset; wheel-over-list should reset it.
	h.previewScrollOffset = 5

	// X=10 is inside the list region (leftWidth=42).
	msg := tea.MouseMsg{X: 10, Y: 10, Button: tea.MouseButtonWheelDown}
	model, _ := h.Update(msg)
	h = model.(*Home)

	if h.cursor != 1 {
		t.Fatalf("WheelDown over list: cursor=%d, want 1", h.cursor)
	}
	if h.previewScrollOffset != 0 {
		t.Fatalf("WheelDown over list: previewScrollOffset=%d, want 0 (should reset on cursor move)", h.previewScrollOffset)
	}
}

// Test 3: renderPreviewPane applies previewScrollOffset when slicing the
// captured content — offset=0 shows tail, offset>0 reveals older lines.
func TestPreviewScroll_Render_AppliesOffset(t *testing.T) {
	const numLines = 50
	h, _ := previewScrollSessionWithLines(t, 120, 40, numLines)

	// offset=0: must include the tail line.
	h.previewScrollOffset = 0
	tailRender := h.renderPreviewPane(78, 20)
	if !strings.Contains(tailRender, fmt.Sprintf("line-%d", numLines-1)) {
		t.Fatalf("offset=0 render: expected tail line %q present, got:\n%s", fmt.Sprintf("line-%d", numLines-1), tailRender)
	}

	// offset=10: tail line must disappear, and a line 10 positions earlier
	// must appear.
	h.previewScrollOffset = 10
	scrolledRender := h.renderPreviewPane(78, 20)
	if strings.Contains(scrolledRender, fmt.Sprintf("line-%d", numLines-1)) {
		t.Fatalf("offset=10 render: tail line %q should NOT be visible, got:\n%s", fmt.Sprintf("line-%d", numLines-1), scrolledRender)
	}
	earlierLine := fmt.Sprintf("line-%d", numLines-1-10)
	if !strings.Contains(scrolledRender, earlierLine) {
		t.Fatalf("offset=10 render: expected earlier line %q visible, got:\n%s", earlierLine, scrolledRender)
	}
}

// Test 4: Cursor movement via keyboard (down/j) resets the preview scroll
// offset — otherwise the new session's preview would open at a stale offset.
func TestPreviewScroll_CursorMove_ResetsOffset(t *testing.T) {
	h, _ := previewScrollSessionWithLines(t, 120, 40, 50)

	inst2 := session.NewInstance("second", t.TempDir())
	inst2.Status = session.StatusRunning
	h.instancesMu.Lock()
	h.instances = append(h.instances, inst2)
	h.instanceByID[inst2.ID] = inst2
	h.instancesMu.Unlock()
	h.flatItems = append(h.flatItems, session.Item{Type: session.ItemTypeSession, Session: inst2})

	h.previewScrollOffset = 7

	model, _ := h.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'j'}})
	h = model.(*Home)

	if h.cursor != 1 {
		t.Fatalf("after 'j': cursor=%d, want 1", h.cursor)
	}
	if h.previewScrollOffset != 0 {
		t.Fatalf("after 'j': previewScrollOffset=%d, want 0 (should reset on cursor move)", h.previewScrollOffset)
	}
}

// Test 5: Render clamps previewScrollOffset to the valid content range and
// does not panic on absurd values.
func TestPreviewScroll_ClampsToContentRange(t *testing.T) {
	h, _ := previewScrollSessionWithLines(t, 120, 40, 20)

	// Over-large offset must clamp during render. We don't assert the exact
	// clamped value (depends on header lines + maxLines), only that the
	// render succeeds AND shows the top of content (line-0) AND the clamped
	// offset is not absurd.
	h.previewScrollOffset = 9999
	rendered := h.renderPreviewPane(78, 20)
	if !strings.Contains(rendered, "line-0") {
		t.Fatalf("offset=9999 should clamp so top-of-content (line-0) is visible, got:\n%s", rendered)
	}
	if h.previewScrollOffset >= 9999 {
		t.Fatalf("previewScrollOffset was not clamped after render: still %d", h.previewScrollOffset)
	}
	if h.previewScrollOffset < 0 {
		t.Fatalf("previewScrollOffset became negative: %d", h.previewScrollOffset)
	}

	// WheelDown (decrement) past zero must clamp at 0.
	h.previewScrollOffset = 0
	msg := tea.MouseMsg{X: 100, Y: 10, Button: tea.MouseButtonWheelDown}
	model, _ := h.Update(msg)
	h = model.(*Home)
	if h.previewScrollOffset < 0 {
		t.Fatalf("WheelDown from 0: previewScrollOffset=%d, want 0 (no negative)", h.previewScrollOffset)
	}
}

// Test 6: In single/stacked layout modes the mouse-wheel-over-preview route
// does NOT apply (there's either no preview or the region detection differs).
// In single mode the wheel must keep list-scroll semantics for all X values.
func TestPreviewScroll_SingleLayoutMode_WheelMovesCursor(t *testing.T) {
	// width=45 → LayoutModeSingle (<50).
	h, _ := previewScrollSessionWithLines(t, 45, 40, 50)

	inst2 := session.NewInstance("second", t.TempDir())
	inst2.Status = session.StatusRunning
	h.instancesMu.Lock()
	h.instances = append(h.instances, inst2)
	h.instanceByID[inst2.ID] = inst2
	h.instancesMu.Unlock()
	h.flatItems = append(h.flatItems, session.Item{Type: session.ItemTypeSession, Session: inst2})

	msg := tea.MouseMsg{X: 30, Y: 10, Button: tea.MouseButtonWheelDown}
	model, _ := h.Update(msg)
	h = model.(*Home)

	if h.cursor != 1 {
		t.Fatalf("single-layout WheelDown: cursor=%d, want 1 (should move cursor since no preview region)", h.cursor)
	}
	if h.previewScrollOffset != 0 {
		t.Fatalf("single-layout WheelDown: previewScrollOffset=%d, want 0 (no preview scroll in single layout)", h.previewScrollOffset)
	}
}
