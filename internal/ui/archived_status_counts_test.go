package ui

// Archiving tears down a session's pane, so an archived row reads as stopped,
// or as error when it was already dead when archived. The status filters only
// ever show active rows, so a pill that counted archived rows offered a number
// its own filter could not show: "✕ 23 error", then "No sessions match".

import (
	"strings"
	"testing"
	"time"

	"github.com/asheshgoplani/agent-deck/internal/session"
)

func TestStatusCounts_ExcludeArchivedSessions(t *testing.T) {
	home := NewHome()
	home.width, home.height = 100, 30

	archivedErrored := &session.Instance{ID: "a1", Title: "archived-errored", Tool: "claude", Status: session.StatusError, ArchivedAt: time.Now()}
	archivedStopped := &session.Instance{ID: "a2", Title: "archived-stopped", Tool: "claude", Status: session.StatusStopped, ArchivedAt: time.Now()}
	activeErrored := &session.Instance{ID: "e1", Title: "active-errored", Tool: "claude", Status: session.StatusError}
	home.instances = []*session.Instance{archivedErrored, archivedStopped, activeErrored}
	home.refreshSessionRenderSnapshot(nil)
	home.cachedStatusCounts.valid.Store(false)

	running, waiting, idle, stopped, errored := home.countSessionStatuses()
	if running != 0 || waiting != 0 || idle != 0 || stopped != 0 || errored != 1 {
		t.Fatalf("counts = running=%d waiting=%d idle=%d stopped=%d errored=%d, want only the active errored session",
			running, waiting, idle, stopped, errored)
	}
}

func TestStatusCounts_ExcludeArchivedRemoteSessions(t *testing.T) {
	home := NewHome()
	home.remoteSessions = map[string][]session.RemoteSessionInfo{
		"box": {
			{ID: "r1", Title: "remote-archived", Status: string(session.StatusError), Archived: true},
			{ID: "r2", Title: "remote-active", Status: string(session.StatusError)},
		},
	}
	home.refreshSessionRenderSnapshot(nil)
	home.cachedStatusCounts.valid.Store(false)

	if _, _, _, _, errored := home.countSessionStatuses(); errored != 1 {
		t.Fatalf("errored = %d, want 1 (the archived remote row must not count)", errored)
	}
}

func TestGroupPreview_CountsOnlyTheShownArchivePartition(t *testing.T) {
	home := NewHome()
	home.width, home.height = 100, 30

	archived := &session.Instance{ID: "a1", Title: "archived-one", Tool: "claude", Status: session.StatusError, ArchivedAt: time.Now()}
	active := &session.Instance{ID: "i1", Title: "active-one", Tool: "claude", Status: session.StatusIdle}
	group := &session.Group{Name: "My Sessions", Path: session.DefaultGroupPath, Sessions: []*session.Instance{archived, active}}

	preview := home.renderGroupPreview(group, 60, 30)
	if !strings.Contains(preview, "1 sessions") || strings.Contains(preview, "error") || strings.Contains(preview, "archived-one") {
		t.Fatalf("active-view preview must leave the archived session out:\n%s", preview)
	}

	home.statusFilter = FilterModeArchived
	preview = home.renderGroupPreview(group, 60, 30)
	if !strings.Contains(preview, "1 sessions") || !strings.Contains(preview, "archived-one") || strings.Contains(preview, "active-one") {
		t.Fatalf("archived-view preview must show only the archived session:\n%s", preview)
	}
}
