package ui

import (
	"testing"
	"time"

	"github.com/asheshgoplani/agent-deck/internal/session"
)

func TestCrossHarnessSupersessionActiveViewShowsOneReplacementRow(t *testing.T) {
	source := &session.Instance{ID: "source", Title: "source", Tool: "claude", Status: session.StatusStopped, GroupPath: session.DefaultGroupPath, CreatedAt: time.Now(), ArchivedAt: time.Now(), SupersededBy: "target"}
	target := &session.Instance{ID: "target", Title: "source", Tool: "codex", Status: session.StatusStopped, GroupPath: session.DefaultGroupPath, CreatedAt: time.Now(), Supersedes: "source"}
	home := NewHome()
	home.instances = []*session.Instance{source, target}
	home.instanceByID = map[string]*session.Instance{source.ID: source, target.ID: target}
	home.groupTree = session.NewGroupTree(home.instances)
	home.rebuildFlatItems()
	visible := visibleSessionIDsFromFlat(home)
	if len(visible) != 1 || visible[0] != target.ID {
		t.Fatalf("active replacement rows = %v, want target only", visible)
	}
}
