package ui

import (
	"strings"
	"testing"

	"github.com/asheshgoplani/agent-deck/internal/session"
)

func makeTabStripTestInstances(n int) []*session.Instance {
	names := []string{"api-service", "frontend", "auth-refactor", "db-migration"}
	instances := make([]*session.Instance, n)
	for i := 0; i < n; i++ {
		name := names[i%len(names)]
		instances[i] = &session.Instance{
			ID:          "inst-" + name,
			Title:       name,
			ProjectPath: "/tmp/" + name,
		}
	}
	return instances
}

func TestNewTabStrip(t *testing.T) {
	ts := NewTabStrip("vertical", 20, true)
	if ts == nil {
		t.Fatal("NewTabStrip returned nil")
	}
	if ts.layout != TabStripVertical {
		t.Errorf("expected vertical layout, got %s", ts.layout)
	}
	if ts.width != 20 {
		t.Errorf("expected width 20, got %d", ts.width)
	}
	if ts.showHotkeys != true {
		t.Error("expected showHotkeys true")
	}
	if ts.selectedIdx != 0 {
		t.Error("expected selectedIdx 0")
	}
	if ts.animFrame != 0 {
		t.Error("expected animFrame 0")
	}
}

func TestTabStripUpdateInstances(t *testing.T) {
	ts := NewTabStrip("vertical", 20, true)
	instances := makeTabStripTestInstances(3)
	ts.UpdateInstances(instances)
	if len(ts.instances) != 3 {
		t.Errorf("expected 3 instances, got %d", len(ts.instances))
	}
	// Update with fewer
	ts.UpdateInstances(makeTabStripTestInstances(2))
	if len(ts.instances) != 2 {
		t.Errorf("expected 2 instances, got %d", len(ts.instances))
	}
	// selectedIdx should clamp
	ts.selectedIdx = 5
	ts.UpdateInstances(makeTabStripTestInstances(2))
	if ts.selectedIdx != 1 {
		t.Errorf("expected selectedIdx clamped to 1, got %d", ts.selectedIdx)
	}
}

func TestTabStripSelectTab(t *testing.T) {
	ts := NewTabStrip("vertical", 20, true)
	ts.UpdateInstances(makeTabStripTestInstances(4))

	ts.SelectTab(2)
	if ts.selectedIdx != 2 {
		t.Errorf("expected 2, got %d", ts.selectedIdx)
	}

	// Clamp high
	ts.SelectTab(100)
	if ts.selectedIdx != 3 {
		t.Errorf("expected clamped to 3, got %d", ts.selectedIdx)
	}

	// Clamp low
	ts.SelectTab(-1)
	if ts.selectedIdx != 0 {
		t.Errorf("expected clamped to 0, got %d", ts.selectedIdx)
	}
}

func TestTabStripNextPrev(t *testing.T) {
	ts := NewTabStrip("vertical", 20, true)
	ts.UpdateInstances(makeTabStripTestInstances(3))

	ts.SelectTab(0)
	ts.NextTab()
	if ts.selectedIdx != 1 {
		t.Errorf("expected 1, got %d", ts.selectedIdx)
	}

	// Wrap forward
	ts.SelectTab(2)
	ts.NextTab()
	if ts.selectedIdx != 0 {
		t.Errorf("expected wrap to 0, got %d", ts.selectedIdx)
	}

	// Wrap backward
	ts.SelectTab(0)
	ts.PrevTab()
	if ts.selectedIdx != 2 {
		t.Errorf("expected wrap to 2, got %d", ts.selectedIdx)
	}
}

func TestTabStripSelectedInstance(t *testing.T) {
	ts := NewTabStrip("vertical", 20, true)
	ts.UpdateInstances(makeTabStripTestInstances(3))
	ts.SelectTab(1)

	inst := ts.SelectedInstance()
	if inst == nil {
		t.Fatal("expected non-nil instance")
	}
	if inst.Title != "frontend" {
		t.Errorf("expected frontend, got %s", inst.Title)
	}

	id := ts.SelectedInstanceID()
	if id != "inst-frontend" {
		t.Errorf("expected inst-frontend, got %s", id)
	}

	// Empty case
	ts2 := NewTabStrip("vertical", 20, true)
	if ts2.SelectedInstance() != nil {
		t.Error("expected nil for empty tab strip")
	}
	if ts2.SelectedInstanceID() != "" {
		t.Error("expected empty string for empty tab strip")
	}
}

func TestTabStripAnimationTick(t *testing.T) {
	ts := NewTabStrip("vertical", 20, true)
	if ts.animFrame != 0 {
		t.Fatal("expected initial frame 0")
	}
	ts.Tick()
	if ts.animFrame != 1 {
		t.Errorf("expected frame 1 after tick, got %d", ts.animFrame)
	}
	for i := 0; i < 10; i++ {
		ts.Tick()
	}
	if ts.animFrame != 11 {
		t.Errorf("expected frame 11, got %d", ts.animFrame)
	}
}

func TestTabStripStatusIcon(t *testing.T) {
	ts := NewTabStrip("vertical", 20, true)

	// Static icons
	tests := []struct {
		status session.Status
		icon   string
	}{
		{session.StatusIdle, "○"},
		{session.StatusError, "✗"},
		{session.StatusStopped, "⏸"},
	}
	for _, tt := range tests {
		got := ts.statusIcon(tt.status, "test-id")
		if got != tt.icon {
			t.Errorf("status %s: expected %s, got %s", tt.status, tt.icon, got)
		}
	}

	// Running should cycle through braille spinner
	icons := make(map[string]bool)
	for i := 0; i < 10; i++ {
		ts.animFrame = i
		icon := ts.statusIcon(session.StatusRunning, "test-id")
		icons[icon] = true
	}
	if len(icons) < 2 {
		t.Error("expected running icon to animate (multiple distinct icons)")
	}

	// Waiting should be static (◐)
	for i := 0; i < 4; i++ {
		ts.animFrame = i
		icon := ts.statusIcon(session.StatusWaiting, "test-id")
		if icon != "◐" {
			t.Errorf("expected waiting icon ◐, got %s", icon)
		}
	}
}

func TestTabStripVerticalView(t *testing.T) {
	ts := NewTabStrip("vertical", 20, true)
	ts.UpdateInstances(makeTabStripTestInstances(4))
	ts.SelectTab(0)

	out := ts.View(10)
	if out == "" {
		t.Fatal("expected non-empty vertical view")
	}
	// Should contain instance names (possibly truncated)
	if !strings.Contains(out, "api") {
		t.Error("expected view to contain 'api'")
	}
}

func TestTabStripHorizontalView(t *testing.T) {
	ts := NewTabStrip("horizontal", 0, true)
	ts.UpdateInstances(makeTabStripTestInstances(4))
	ts.SelectTab(0)

	out := ts.View(80)
	if out == "" {
		t.Fatal("expected non-empty horizontal view")
	}
	if !strings.Contains(out, "api") {
		t.Error("expected view to contain 'api'")
	}
}

func TestTabStripTransition(t *testing.T) {
	ts := NewTabStrip("vertical", 20, true)
	instances := makeTabStripTestInstances(2)
	instances[0].Status = session.StatusIdle
	ts.UpdateInstances(instances)

	// Change status to trigger transition
	instances[0].Status = session.StatusRunning
	ts.UpdateInstances(instances)

	// Should have a transition for the first instance
	if len(ts.transitions) == 0 {
		t.Error("expected transition to be created on status change")
	}

	// Tick through transition
	for i := 0; i < 10; i++ {
		ts.Tick()
	}
}

func TestTabStripUnread(t *testing.T) {
	ts := NewTabStrip("vertical", 20, true)
	instances := makeTabStripTestInstances(2)
	instances[0].Status = session.StatusWaiting
	instances[1].Status = session.StatusIdle
	ts.UpdateInstances(instances)

	// Mark first as acknowledged (not unread), second as unread
	ts.UpdateUnreadState(map[string]bool{"inst-api-service": true})

	// Unread should be set for non-acknowledged instances that are waiting/idle
	// The unread icon for done/unread is ✓
	icon := ts.statusIcon(session.StatusIdle, "inst-frontend")
	// inst-frontend was not acknowledged, if it's in unreadMap it shows ✓
	_ = icon // just verify no panic
}
