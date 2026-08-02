// The TUI hides archived sessions at RENDER time (rebuildFlatItems partitions
// them out of h.flatItems), but publishWebMenuSnapshot builds the web snapshot
// from h.instances — the unfiltered slice. With the web server embedded in the
// TUI (`agent-deck web` without --no-tui), MemoryMenuData serves that published
// snapshot in preference to the storage-backed fallback, so archived sessions
// appeared as ordinary rows in the web sidebar while staying hidden in the TUI.
//
// The storage-backed path has always filtered (session_data_service.go:
// FilterInstancesByArchive(instances, false)); the TUI publish path never did.

package ui

import (
	"testing"
	"time"

	"github.com/asheshgoplani/agent-deck/internal/session"
	"github.com/asheshgoplani/agent-deck/internal/web"
)

// menuSnapshotTitles returns the session titles present in a published snapshot.
func menuSnapshotTitles(snap *web.MenuSnapshot) []string {
	var titles []string
	for _, item := range snap.Items {
		if item.Type == web.MenuItemTypeSession && item.Session != nil {
			titles = append(titles, item.Session.Title)
		}
	}
	return titles
}

func TestPublishWebMenuSnapshotExcludesArchivedSessions(t *testing.T) {
	home := NewHome()
	home.width, home.height = 120, 40
	home.initialLoading = false

	active := session.NewInstanceWithTool("active", "/tmp/a", "claude")
	archived := session.NewInstanceWithTool("archived", "/tmp/b", "claude")
	active.Status = session.StatusIdle
	// An archived session keeps a stale, display-frozen status — `error` is the
	// common case (classifyTerminatedPane returns it for a vanished pane with no
	// recoverable exit code), and it is what paints a red dot in the web UI.
	archived.Status = session.StatusError
	archived.ArchivedAt = time.Now().UTC()

	instances := []*session.Instance{active, archived}
	home.instancesMu.Lock()
	home.instances = instances
	home.instancesMu.Unlock()
	home.groupTree = session.NewGroupTree(instances)

	// nil fallback: the snapshot under test must come from the TUI publish path,
	// never from the storage-backed loader that already filters correctly.
	menuData := web.NewMemoryMenuData(nil)
	home.SetWebMenuData(menuData)
	home.rebuildFlatItems()

	snap, err := menuData.LoadMenuSnapshot()
	if err != nil {
		t.Fatalf("LoadMenuSnapshot: %v", err)
	}

	titles := menuSnapshotTitles(snap)
	for _, title := range titles {
		if title == "archived" {
			t.Errorf("archived session leaked into the published web menu snapshot: %v "+
				"(TUI hides it, web sidebar would render it — with a red dot for status=error)", titles)
		}
	}
	if len(titles) != 1 || titles[0] != "active" {
		t.Errorf("published snapshot sessions = %v, want [active]", titles)
	}
	if snap.TotalSessions != 1 {
		t.Errorf("snapshot.TotalSessions = %d, want 1", snap.TotalSessions)
	}
}

// The archived view is served by a separate endpoint backed by
// LoadArchivedMenuSnapshot, which feeds BuildMenuSnapshot an archived-only
// slice. BuildMenuSnapshot must therefore stay archive-agnostic — filtering
// inside the builder would empty the Archived pane instead of fixing the leak.
func TestBuildMenuSnapshotStaysArchiveAgnostic(t *testing.T) {
	archived := session.NewInstanceWithTool("archived", "/tmp/b", "claude")
	archived.ArchivedAt = time.Now().UTC()

	snap := web.BuildMenuSnapshot("default", []*session.Instance{archived}, nil, time.Now())

	if snap.TotalSessions != 1 {
		t.Fatalf("BuildMenuSnapshot dropped an archived instance (TotalSessions=%d, want 1); "+
			"the archived pane feeds it archived-only input and needs them preserved", snap.TotalSessions)
	}
}
