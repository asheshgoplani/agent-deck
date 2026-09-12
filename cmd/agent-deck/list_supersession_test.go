package main

import (
	"testing"
	"time"

	"github.com/asheshgoplani/agent-deck/internal/session"
)

func TestDefaultListInstancesShowsOneSuccessfulReplacementAndSupportsUnarchiveRecovery(t *testing.T) {
	source := &session.Instance{ID: "source", ArchivedAt: time.Now(), SupersededBy: "target"}
	target := &session.Instance{ID: "target", Supersedes: "source"}
	visible := defaultListInstances([]*session.Instance{source, target})
	if len(visible) != 1 || visible[0].ID != "target" {
		t.Fatalf("successful replacement default rows = %#v, want target only", visible)
	}

	// Existing unarchive is the explicit recovery action: no replay is needed
	// and the retained source must not remain permanently invisible.
	source.ArchivedAt = time.Time{}
	visible = defaultListInstances([]*session.Instance{source, target})
	if len(visible) != 2 {
		t.Fatalf("unarchived source remained hidden: %#v", visible)
	}
}

func TestDefaultListInstancesKeepsArchivedPendingTargetAndNormalArchives(t *testing.T) {
	pendingTarget := &session.Instance{ID: "pending", ArchivedAt: time.Now(), Supersedes: "source"}
	normalArchive := &session.Instance{ID: "normal", ArchivedAt: time.Now()}
	visible := defaultListInstances([]*session.Instance{pendingTarget, normalArchive})
	if len(visible) != 2 {
		t.Fatalf("default list changed normal/pending archive visibility: %#v", visible)
	}
}
