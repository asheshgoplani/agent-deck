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

// TestCountByStatusMatchesListTotal is finding 1's CLI-side fixture
// (2026-09-18 live UI audit): `status --json`'s total used to exceed `list
// --json`'s by counting archived cross-harness sources still superseded by
// a "Restart with new session ID" (the source stays in storage, its old
// tmux process often still alive, but list --json correctly hides it). Both
// commands must report on exactly the same tracked set.
func TestCountByStatusMatchesListTotal(t *testing.T) {
	source := &session.Instance{ID: "source", Status: session.StatusIdle, ArchivedAt: time.Now(), SupersededBy: "target"}
	target := &session.Instance{ID: "target", Status: session.StatusWaiting, Supersedes: "source"}
	untouched := &session.Instance{ID: "plain", Status: session.StatusRunning}
	all := []*session.Instance{source, target, untouched}

	listVisible := defaultListInstances(all)
	if len(listVisible) != 2 {
		t.Fatalf("precondition: list --json set = %d, want 2 (source hidden)", len(listVisible))
	}

	counts := countByStatus(all)
	if counts.total != len(listVisible) {
		t.Fatalf("status --json total = %d, list --json total = %d; they must agree (source %q leaked into the count)", counts.total, len(listVisible), source.ID)
	}
	if counts.total != 2 {
		t.Fatalf("counts = %+v, want total=2 (source excluded)", counts)
	}
}
