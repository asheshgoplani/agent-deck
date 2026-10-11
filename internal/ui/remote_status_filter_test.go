package ui

// Regression tests for #2627: the status filter keys (%, &, ^) filtered only
// local sessions; rows under remote hosts rendered regardless of their status.
// The fix runs remote rows through the same predicate (using Archived for the
// archived partition) and narrows the time-filter fallback the same way it
// narrows local sessions.

import (
	"strings"
	"testing"
	"time"

	"github.com/asheshgoplani/agent-deck/internal/session"
)

func makeRemoteFilterHome(sessions ...session.RemoteSessionInfo) *Home {
	h := seamBNewHome()
	h.activeFilterExcludes = (session.DisplaySettings{}).GetActiveFilterExcludes()
	h.remoteSessions = map[string][]session.RemoteSessionInfo{"mac": sessions}
	h.remoteGroups = map[string][]string{"mac": {"alpha"}}
	return h
}

func remoteRowTitles(h *Home) []string {
	titles := make([]string, 0, len(h.flatItems))
	for _, item := range h.flatItems {
		if item.Type == session.ItemTypeRemoteSession && item.RemoteSession != nil {
			titles = append(titles, item.RemoteSession.Title)
		}
	}
	return titles
}

func TestRemoteRowsHonorActiveStatusFilter(t *testing.T) {
	h := makeRemoteFilterHome(
		session.RemoteSessionInfo{ID: "r1", Title: "running", Status: "running", Group: "alpha"},
		session.RemoteSessionInfo{ID: "r2", Title: "idle", Status: "idle", Group: "alpha"},
		session.RemoteSessionInfo{ID: "r3", Title: "broken", Status: "error", Group: "alpha"},
		session.RemoteSessionInfo{ID: "r4", Title: "dead", Status: "stopped", Group: "alpha"},
		session.RemoteSessionInfo{ID: "r5", Title: "old", Status: "running", Group: "alpha", Archived: true},
	)
	h.statusFilter = FilterModeActive
	h.rebuildFlatItems()
	got := strings.Join(remoteRowTitles(h), ",")
	if got != "running,idle" {
		t.Fatalf("active filter kept %q, want running,idle", got)
	}
}

func TestRemoteRowsHonorErrorStatusFilter(t *testing.T) {
	h := makeRemoteFilterHome(
		session.RemoteSessionInfo{ID: "r1", Title: "running", Status: "running", Group: "alpha"},
		session.RemoteSessionInfo{ID: "r2", Title: "broken", Status: "error", Group: "alpha"},
	)
	h.statusFilter = session.StatusError
	h.rebuildFlatItems()
	got := strings.Join(remoteRowTitles(h), ",")
	if got != "broken" {
		t.Fatalf("error filter kept %q, want broken", got)
	}
}

func TestRemoteRowsHonorArchivedView(t *testing.T) {
	h := makeRemoteFilterHome(
		session.RemoteSessionInfo{ID: "r1", Title: "running", Status: "running", Group: "alpha"},
		session.RemoteSessionInfo{ID: "r2", Title: "old", Status: "error", Group: "alpha", Archived: true},
	)
	h.statusFilter = FilterModeArchived
	h.rebuildFlatItems()
	got := strings.Join(remoteRowTitles(h), ",")
	if got != "old" {
		t.Fatalf("archived view kept %q, want old", got)
	}
}

func TestRemoteOldStatusKeepsCoarseBehavior(t *testing.T) {
	// Old remotes omit substate/archived; an empty status degrades to the
	// same idle bucket the row glyph shows (#2627 requirement), so a
	// FilterModeActive view keeps the row.
	h := makeRemoteFilterHome(
		session.RemoteSessionInfo{ID: "r1", Title: "legacy", Status: "", Group: "alpha"},
	)
	h.statusFilter = FilterModeActive
	h.rebuildFlatItems()
	got := strings.Join(remoteRowTitles(h), ",")
	if got != "legacy" {
		t.Fatalf("old-remote row dropped by active filter: %q, want legacy", got)
	}
}

func TestTimeFilterFallbackRespectsStatusFilter(t *testing.T) {
	recent := time.Now().Format(time.RFC3339Nano)

	// An error row is a time candidate but a status-filter miss: it must
	// count as neither a candidate nor a match in the fallback — the exact
	// behavior the local branch has — so it stays hidden rather than
	// pinning the time filter (#2627, maintainer's review point).
	h := makeRemoteFilterHome(
		session.RemoteSessionInfo{ID: "r1", Title: "broken", Status: "error", Group: "alpha", LastActivityAt: recent},
	)
	h.statusFilter = FilterModeActive
	h.timeFilter = session.TimeFilterToday
	h.rebuildFlatItems()
	if got := strings.Join(remoteRowTitles(h), ","); got != "" {
		t.Fatalf("filtered-out row must not survive both filters, kept %q", got)
	}

	// A running row is both a status match and a time candidate: it stays
	// visible and the time filter is not reset.
	h2 := makeRemoteFilterHome(
		session.RemoteSessionInfo{ID: "r1", Title: "running", Status: "running", Group: "alpha", LastActivityAt: recent},
	)
	h2.statusFilter = FilterModeActive
	h2.timeFilter = session.TimeFilterToday
	h2.rebuildFlatItems()
	if h2.timeFilter != session.TimeFilterToday {
		t.Fatalf("time filter reset despite a row matching both filters, got %v", h2.timeFilter)
	}
	if got := strings.Join(remoteRowTitles(h2), ","); got != "running" {
		t.Fatalf("row matching both filters must stay visible, kept %q", got)
	}
}

// The empty-filter fallback predates this PR: with no LOCAL match it cleared
// the status filter, which would then bypass the remote filtering added for
// #2627 and show every remote row. Remote matches now count toward the
// fallback the way local matches do (CodeRabbit, PR #2645).
func TestEmptyFilterFallbackCountsRemoteMatches(t *testing.T) {
	// No local session matches; a remote error row does. The filter must
	// survive — the local list stays empty and the remote match renders.
	local := session.NewInstanceWithTool("local-ok", "/tmp/local-ok", "claude")
	local.ID = "local-ok"
	local.GroupPath = "alpha"
	local.Status = session.StatusIdle
	h := makeRemoteFilterHome(
		session.RemoteSessionInfo{ID: "r1", Title: "broken", Status: "error", Group: "alpha"},
	)
	h.groupTree = session.NewGroupTree([]*session.Instance{local})
	h.statusFilter = session.StatusError
	h.rebuildFlatItems()
	if h.statusFilter == "" {
		t.Fatal("fallback cleared the filter despite a remote match")
	}
	got := strings.Join(remoteRowTitles(h), ",")
	if got != "broken" {
		t.Fatalf("remote match hidden by the fallback, kept %q", got)
	}

	// No match anywhere: the fallback still clears (pre-existing behavior).
	h2 := makeRemoteFilterHome(
		session.RemoteSessionInfo{ID: "r1", Title: "running", Status: "running", Group: "alpha"},
	)
	h2.groupTree = session.NewGroupTree([]*session.Instance{local})
	h2.statusFilter = session.StatusError
	h2.rebuildFlatItems()
	if h2.statusFilter != "" {
		t.Fatalf("fallback should clear the filter with no matches anywhere, got %q", h2.statusFilter)
	}
}

func TestEmptyFilterFallbackKeepsArchivedViewWithRemoteArchived(t *testing.T) {
	local := session.NewInstanceWithTool("local-ok", "/tmp/local-ok", "claude")
	local.ID = "local-ok"
	local.GroupPath = "alpha"
	local.Status = session.StatusIdle
	h := makeRemoteFilterHome(
		session.RemoteSessionInfo{ID: "r1", Title: "old", Status: "error", Group: "alpha", Archived: true},
	)
	h.groupTree = session.NewGroupTree([]*session.Instance{local})
	h.statusFilter = FilterModeArchived
	h.rebuildFlatItems()
	if h.statusFilter == "" {
		t.Fatal("fallback cleared the archived view despite a remote archived row")
	}
	got := strings.Join(remoteRowTitles(h), ",")
	if got != "old" {
		t.Fatalf("remote archived row hidden by the fallback, kept %q", got)
	}
}
