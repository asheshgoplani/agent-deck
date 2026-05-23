package ui

import (
	"fmt"
	"strings"
	"testing"
)

// TestKanbanPanel_NilSafe verifies that nil-receiver methods don't panic.
func TestKanbanPanel_NilSafe(t *testing.T) {
	var p *KanbanPanel
	if p.IsVisible() {
		t.Error("nil panel should not be visible")
	}
	// None of these should panic
	p.Show()
	p.Hide()
	p.SetSize(120, 40)
	p.SetTasks(nil, "")
	if p.Toggle() {
		t.Error("nil panel toggle should return false")
	}
	if p.View() != "" {
		t.Error("nil panel View should return empty string")
	}
}

// TestKanbanPanel_ToggleVisibility verifies that Toggle flips IsVisible.
func TestKanbanPanel_ToggleVisibility(t *testing.T) {
	p := NewKanbanPanel()
	if p.IsVisible() {
		t.Error("new panel should not be visible")
	}
	if !p.Toggle() {
		t.Error("first Toggle should return true (now visible)")
	}
	if !p.IsVisible() {
		t.Error("panel should be visible after first toggle")
	}
	if p.Toggle() {
		t.Error("second Toggle should return false (now hidden)")
	}
	if p.IsVisible() {
		t.Error("panel should not be visible after second toggle")
	}
}

// TestKanbanPanel_ShowSetsLoading verifies that Show marks the panel as loading.
func TestKanbanPanel_ShowSetsLoading(t *testing.T) {
	p := NewKanbanPanel()
	p.Show()
	if !p.loading {
		t.Error("Show should set loading=true")
	}
	if p.fetchErr != "" {
		t.Error("Show should clear fetchErr")
	}
}

// TestKanbanPanel_SetTasksClearsLoading verifies that SetTasks marks loading done.
func TestKanbanPanel_SetTasksClearsLoading(t *testing.T) {
	p := NewKanbanPanel()
	p.Show()
	p.SetTasks([]KanbanTask{{ID: "T1", Title: "Fix login", Status: "running"}}, "")
	if p.loading {
		t.Error("SetTasks should clear loading flag")
	}
	if len(p.tasks) != 1 {
		t.Errorf("expected 1 task, got %d", len(p.tasks))
	}
}

// TestKanbanPanel_SetTasksWithError verifies that error string is stored.
func TestKanbanPanel_SetTasksWithError(t *testing.T) {
	p := NewKanbanPanel()
	p.Show()
	p.SetTasks(nil, "hermes not found")
	if p.fetchErr != "hermes not found" {
		t.Errorf("fetchErr = %q, want %q", p.fetchErr, "hermes not found")
	}
}

// TestKanbanPanel_ViewHidden returns empty when not visible.
func TestKanbanPanel_ViewHidden(t *testing.T) {
	p := NewKanbanPanel()
	p.SetSize(120, 40)
	if got := p.View(); got != "" {
		t.Errorf("hidden panel View() = %q, want empty", got)
	}
}

// TestKanbanPanel_ViewVisible returns non-empty string when visible.
func TestKanbanPanel_ViewVisible(t *testing.T) {
	p := NewKanbanPanel()
	p.SetSize(120, 40)
	p.Show()
	p.SetTasks([]KanbanTask{
		{ID: "T1", Title: "Running task", Status: "running"},
		{ID: "T2", Title: "Blocked task", Status: "blocked", BlockReason: "waiting for API key"},
	}, "")
	view := p.View()
	if view == "" {
		t.Error("visible panel View() should return non-empty string")
	}
	if !strings.Contains(view, "RUNNING") {
		t.Error("View should contain RUNNING header")
	}
	if !strings.Contains(view, "BLOCKED") {
		t.Error("View should contain BLOCKED header")
	}
}

// TestKanbanPanel_ViewShowsErrorWhenSet verifies error text appears in view.
func TestKanbanPanel_ViewShowsErrorWhenSet(t *testing.T) {
	p := NewKanbanPanel()
	p.SetSize(80, 30)
	p.Show()
	p.SetTasks(nil, "hermes not found")
	view := p.View()
	if !strings.Contains(view, "hermes not found") {
		t.Errorf("View should show error text; got: %s", view)
	}
}

// TestKanbanPanel_ViewShowsLoadingWhenLoading verifies loading state shows in view.
func TestKanbanPanel_ViewShowsLoadingWhenLoading(t *testing.T) {
	p := NewKanbanPanel()
	p.SetSize(80, 30)
	p.Show() // sets loading=true
	view := p.View()
	if !strings.Contains(view, "loading") {
		t.Errorf("View should show 'loading' when loading=true; got: %s", view)
	}
}

// TestTruncate verifies string truncation.
func TestTruncate(t *testing.T) {
	tests := []struct {
		s    string
		maxW int
		want string
	}{
		{"hello", 10, "hello"},
		{"hello world", 8, "hello w…"},
		{"hi", 3, "hi"},
		{"hello", 0, "hello"},   // maxW ≤ 3 → passthrough
		{"hello", 3, "hello"},   // maxW == 3 → passthrough
		{"hello", 4, "hel…"},
	}
	for _, tt := range tests {
		got := truncate(tt.s, tt.maxW)
		if got != tt.want {
			t.Errorf("truncate(%q, %d) = %q, want %q", tt.s, tt.maxW, got, tt.want)
		}
	}
}

// TestPad verifies right-padding.
func TestPad(t *testing.T) {
	tests := []struct {
		s     string
		width int
		want  string
	}{
		{"hi", 5, "hi   "},
		{"hello", 5, "hello"},
		{"toolong", 4, "toolong"}, // longer than width — no truncation
		{"", 3, "   "},
	}
	for _, tt := range tests {
		got := pad(tt.s, tt.width)
		if got != tt.want {
			t.Errorf("pad(%q, %d) = %q, want %q", tt.s, tt.width, got, tt.want)
		}
	}
}

// TestIsHermesNotFound verifies error classification.
func TestIsHermesNotFound(t *testing.T) {
	tests := []struct {
		msg  string
		want bool
	}{
		{"exec: \"hermes\": executable file not found in $PATH", true},
		{"fork/exec /usr/bin/hermes: no such file or directory", true},
		{"hermes: not found", true},
		{"hermes: exit status 1", false},
		{"connection refused", false},
		{"", false},
	}
	for _, tt := range tests {
		var err error
		if tt.msg != "" {
			err = fmt.Errorf("%s", tt.msg)
		}
		got := isHermesNotFound(err)
		if got != tt.want {
			t.Errorf("isHermesNotFound(%q) = %v, want %v", tt.msg, got, tt.want)
		}
	}
}
