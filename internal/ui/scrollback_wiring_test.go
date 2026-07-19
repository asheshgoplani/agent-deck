package ui

import (
	"errors"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
)

// TestScrollbackContentMsg_StaleGuard verifies a capture that completes for a
// different (or already-closed) session does not overwrite the pager, while a
// matching capture installs its content.
func TestScrollbackContentMsg_StaleGuard(t *testing.T) {
	h := NewHome()
	h.width, h.height = 80, 10
	h.initialLoading = false
	h.scrollbackPager.Show("s", "id-1", h.width, h.height)

	// Stale: a capture for a different session must be ignored.
	m, _ := h.Update(scrollbackContentMsg{sessionID: "id-2", content: "other\ncontent"})
	h = m.(*Home)
	if !h.scrollbackPager.loading {
		t.Error("stale capture should leave the pager in its loading state")
	}

	// Matching: installs content and clears loading.
	m, _ = h.Update(scrollbackContentMsg{sessionID: "id-1", content: "a\nb\nc"})
	h = m.(*Home)
	if h.scrollbackPager.loading {
		t.Error("matching capture should clear loading")
	}
	if len(h.scrollbackPager.lines) != 3 {
		t.Errorf("matching capture: got %d lines, want 3", len(h.scrollbackPager.lines))
	}

	// Error capture surfaces the error.
	h.scrollbackPager.Show("s", "id-3", h.width, h.height)
	m, _ = h.Update(scrollbackContentMsg{sessionID: "id-3", err: errors.New("boom")})
	h = m.(*Home)
	if h.scrollbackPager.errText != "boom" {
		t.Errorf("error capture: errText = %q, want boom", h.scrollbackPager.errText)
	}
}

// TestScrollbackContentMsg_IgnoredWhenClosed verifies a late capture after the
// pager closed is dropped.
func TestScrollbackContentMsg_IgnoredWhenClosed(t *testing.T) {
	h := NewHome()
	h.width, h.height = 80, 10
	h.initialLoading = false
	// Pager not visible.
	m, _ := h.Update(scrollbackContentMsg{sessionID: "id-1", content: "x"})
	h = m.(*Home)
	if h.scrollbackPager.IsVisible() {
		t.Error("content for a closed pager must not reopen it")
	}
}

// TestScrollbackPagerKey_Routing verifies navigation keys scroll and Ctrl+Q
// closes the pager to the list without re-attaching.
func TestScrollbackPagerKey_Routing(t *testing.T) {
	h := NewHome()
	h.width, h.height = 80, 10 // body height 8
	h.initialLoading = false
	h.scrollbackPager.Show("s", "id", h.width, h.height)
	// 20 lines => maxOffset 12, pinned to bottom.
	content := ""
	for i := 0; i < 20; i++ {
		content += "row\n"
	}
	h.scrollbackPager.SetContent(content)
	if h.scrollbackPager.offset != 12 {
		t.Fatalf("setup: offset = %d, want 12", h.scrollbackPager.offset)
	}

	// 'g' jumps to the top.
	m, _ := h.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'g'}})
	h = m.(*Home)
	if h.scrollbackPager.offset != 0 {
		t.Errorf("after 'g': offset = %d, want 0", h.scrollbackPager.offset)
	}

	// 'j' scrolls down one.
	m, _ = h.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'j'}})
	h = m.(*Home)
	if h.scrollbackPager.offset != 1 {
		t.Errorf("after 'j': offset = %d, want 1", h.scrollbackPager.offset)
	}

	// Ctrl+Q closes to the list without re-attaching.
	m, _ = h.Update(tea.KeyMsg{Type: tea.KeyCtrlQ})
	h = m.(*Home)
	if h.scrollbackPager.IsVisible() {
		t.Error("Ctrl+Q should close the pager")
	}
}

// TestScrollbackPagerKey_EscReattaches verifies Esc closes the pager (the
// re-attach command targets the bound session).
func TestScrollbackPagerKey_EscReattaches(t *testing.T) {
	h := NewHome()
	h.width, h.height = 80, 10
	h.initialLoading = false
	h.scrollbackPager.Show("s", "missing-id", h.width, h.height)
	h.scrollbackPager.SetContent("a\nb")

	// Esc closes the pager. The re-attach command is a no-op here because the
	// session id isn't in the instance map, but the pager must close.
	m, _ := h.Update(tea.KeyMsg{Type: tea.KeyEsc})
	h = m.(*Home)
	if h.scrollbackPager.IsVisible() {
		t.Error("Esc should close the pager")
	}
}
