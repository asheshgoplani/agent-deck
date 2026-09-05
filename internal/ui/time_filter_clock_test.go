package ui

import (
	"testing"
	"time"

	"github.com/asheshgoplani/agent-deck/internal/session"
	"github.com/stretchr/testify/require"
)

func clockFilterHome(mode session.TimeFilterMode, locals []*session.Instance, remotes []session.RemoteSessionInfo) *Home {
	h := NewHome()
	h.width, h.height, h.initialLoading = 120, 40, false
	h.instances = locals
	h.groupTree = session.NewGroupTree(locals)
	h.remoteSessions = map[string][]session.RemoteSessionInfo{"dev": remotes}
	h.timeFilter = mode
	h.groupViewMode = session.GroupViewNormal
	h.rebuildFlatItems()
	return h
}

func clockFilterTick(h *Home, at time.Time) {
	// Exercise real Update without dispatching unrelated background commands.
	now := time.Now()
	h.lastLogCheck, h.lastCachePrune = now, now
	h.agentsLoaded, h.agentsLastRefresh = true, now
	h.uiStateSaveTicks = 0
	h.Update(tickMsg(at))
}

func clockLocal(id string, at time.Time) *session.Instance {
	return &session.Instance{ID: id, Title: id, GroupPath: "work", LastAccessedAt: at, Status: session.StatusIdle}
}

func clockVisibleIDs(h *Home) []string {
	var ids []string
	for _, item := range h.flatItems {
		if item.Type == session.ItemTypeSession && item.Session != nil {
			ids = append(ids, item.Session.ID)
		}
		if item.Type == session.ItemTypeRemoteSession && item.RemoteSession != nil {
			ids = append(ids, item.RemoteSession.ID)
		}
	}
	return ids
}

func TestTimeFilterClockExpiresAndPreservesSelection(t *testing.T) {
	for _, mode := range []session.TimeFilterMode{session.TimeFilterToday, session.TimeFilter3Days, session.TimeFilter7Days} {
		for _, remote := range []bool{false, true} {
			name := mode.Label() + "/local"
			if remote {
				name = mode.Label() + "/remote"
			}
			t.Run(name, func(t *testing.T) {
				now := time.Now()
				activity, boundary := now, time.Date(now.Year(), now.Month(), now.Day()+1, 0, 0, 0, 0, now.Location())
				if mode != session.TimeFilterToday {
					days := 3
					if mode == session.TimeFilter7Days {
						days = 7
					}
					activity = now.Add(-time.Duration(days)*24*time.Hour + time.Hour)
					boundary = activity.Add(time.Duration(days) * 24 * time.Hour)
				}
				keeper := boundary.Add(time.Hour)
				locals := []*session.Instance{clockLocal("expires", activity), clockLocal("selected", keeper)}
				var remotes []session.RemoteSessionInfo
				if remote {
					locals = nil
					remotes = []session.RemoteSessionInfo{{ID: "expires", RemoteName: "dev", Group: "work", LastActivityAt: activity.Format(time.RFC3339Nano)}, {ID: "selected", RemoteName: "dev", Group: "work", LastActivityAt: keeper.Format(time.RFC3339Nano)}}
				}
				h := clockFilterHome(mode, locals, remotes)
				require.ElementsMatch(t, []string{"expires", "selected"}, clockVisibleIDs(h))
				for n, item := range h.flatItems {
					if (item.Session != nil && item.Session.ID == "selected") || (item.RemoteSession != nil && item.RemoteSession.ID == "selected") {
						h.cursor = n
					}
				}
				selected := h.captureSelectedItemIdentity()
				// Rolling comparisons include the exact cutoff; Today changes at midnight.
				before := boundary.Add(-time.Nanosecond)
				if mode != session.TimeFilterToday {
					before = boundary
				}
				h.jumpMode, h.jumpBuffer = true, "2"
				first := &h.flatItems[0]
				clockFilterTick(h, before)
				require.ElementsMatch(t, []string{"expires", "selected"}, clockVisibleIDs(h))
				require.Equal(t, "2", h.jumpBuffer, "a pre-boundary tick must not reset jump input")
				require.Same(t, first, &h.flatItems[0], "a pre-boundary tick must not rebuild rows")
				clockFilterTick(h, boundary.Add(time.Nanosecond))
				require.Equal(t, []string{"selected"}, clockVisibleIDs(h), "clock expiry must remove old membership")
				require.Equal(t, selected, h.captureSelectedItemIdentity())
				require.Equal(t, mode, h.timeFilter)
			})
		}
	}
}

func TestTimeFilterClockExpiryClearsEmptyFilter(t *testing.T) {
	for _, mode := range []session.TimeFilterMode{session.TimeFilterToday, session.TimeFilter3Days, session.TimeFilter7Days} {
		t.Run(mode.Label(), func(t *testing.T) {
			now := time.Now()
			h := clockFilterHome(mode, []*session.Instance{clockLocal("only", now)}, nil)
			clockFilterTick(h, now.AddDate(0, 0, 8))
			require.Equal(t, session.TimeFilterAll, h.timeFilter, "last matching row expired; fallback must run")
			require.Equal(t, []string{"only"}, clockVisibleIDs(h))
		})
	}
}

func TestTimeFilterClockCollapsedAndUnknownRemote(t *testing.T) {
	now := time.Now()
	h := clockFilterHome(session.TimeFilter3Days, []*session.Instance{clockLocal("collapsed", now.Add(-72*time.Hour+time.Hour))}, []session.RemoteSessionInfo{{ID: "unknown", RemoteName: "dev", LastActivityAt: "invalid"}})
	h.groupTree.GroupList[0].Expanded = false
	h.rebuildFlatItems()
	clockFilterTick(h, now.Add(2*time.Hour))
	for _, item := range h.flatItems {
		require.False(t, item.Type == session.ItemTypeGroup && item.Path == "work", "expired collapsed group must disappear")
	}
	require.Equal(t, []string{"unknown"}, clockVisibleIDs(h))
	require.Equal(t, session.TimeFilter3Days, h.timeFilter, "unknown remote remains eligible")
	h.jumpMode, h.jumpBuffer = true, "2"
	clockFilterTick(h, now.Add(3*time.Hour))
	require.Equal(t, "2", h.jumpBuffer, "unknown-only results have no next expiry")
}

func TestTimeFilterClockAllDoesNotRebuild(t *testing.T) {
	h := clockFilterHome(session.TimeFilterAll, []*session.Instance{clockLocal("all", time.Now())}, nil)
	h.jumpMode, h.jumpBuffer = true, "2"
	first := &h.flatItems[0]
	clockFilterTick(h, time.Now().AddDate(0, 0, 30))
	require.Equal(t, "2", h.jumpBuffer)
	require.Same(t, first, &h.flatItems[0])
}
