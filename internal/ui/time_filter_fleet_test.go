package ui

import (
	"reflect"
	"sort"
	"testing"
	"time"

	"github.com/asheshgoplani/agent-deck/internal/session"
)

func TestTimeFilterFleetFallback(t *testing.T) {
	now := time.Now()
	old := now.AddDate(0, 0, -30)
	local := func(id, group string, at time.Time) *session.Instance {
		return &session.Instance{ID: id, Title: id, GroupPath: group, Status: session.StatusIdle, LastAccessedAt: at}
	}
	remote := func(id, at string) session.RemoteSessionInfo {
		return session.RemoteSessionInfo{ID: id, Title: id, Group: "work", RemoteName: "dev", LastActivityAt: at}
	}
	cases := []struct {
		name     string
		locals   []*session.Instance
		remotes  []session.RemoteSessionInfo
		scope    string
		archived bool
		wantMode session.TimeFilterMode
		wantIDs  []string
	}{
		{name: "mixed fleet", locals: []*session.Instance{local("local-old", "work", old)}, remotes: []session.RemoteSessionInfo{remote("remote-recent", now.Format(time.RFC3339Nano)), remote("remote-old", old.Format(time.RFC3339Nano))}, wantMode: session.TimeFilter3Days, wantIDs: []string{"remote-recent"}},
		{name: "missing remote timestamp", locals: []*session.Instance{local("local-old", "work", old)}, remotes: []session.RemoteSessionInfo{remote("remote-unknown", ""), remote("remote-old", old.Format(time.RFC3339Nano))}, wantMode: session.TimeFilter3Days, wantIDs: []string{"remote-unknown"}},
		{name: "malformed remote timestamp", locals: []*session.Instance{local("local-old", "work", old)}, remotes: []session.RemoteSessionInfo{remote("remote-unknown", "invalid"), remote("remote-old", old.Format(time.RFC3339Nano))}, wantMode: session.TimeFilter3Days, wantIDs: []string{"remote-unknown"}},
		{name: "all old", locals: []*session.Instance{local("local-old", "work", old)}, remotes: []session.RemoteSessionInfo{remote("remote-old", old.Format(time.RFC3339Nano))}, wantMode: session.TimeFilterAll, wantIDs: []string{"local-old", "remote-old"}},
		{name: "remote only old", remotes: []session.RemoteSessionInfo{remote("remote-old", old.Format(time.RFC3339Nano))}, wantMode: session.TimeFilterAll, wantIDs: []string{"remote-old"}},
		{name: "empty fleet", wantMode: session.TimeFilter3Days},
		{name: "scope excludes recent local", locals: []*session.Instance{local("local-old", "work", old), local("local-recent", "personal", now)}, scope: "work", wantMode: session.TimeFilterAll, wantIDs: []string{"local-old"}},
		{name: "scope retains remote match", locals: []*session.Instance{local("local-old", "work", old), local("local-recent", "personal", now)}, remotes: []session.RemoteSessionInfo{remote("remote-recent", now.Format(time.RFC3339Nano))}, scope: "work", wantMode: session.TimeFilter3Days, wantIDs: []string{"remote-recent"}},
		{name: "archive excludes remotes", locals: []*session.Instance{local("local-old", "work", old)}, remotes: []session.RemoteSessionInfo{remote("remote-recent", now.Format(time.RFC3339Nano))}, archived: true, wantMode: session.TimeFilterAll, wantIDs: []string{"local-old"}},
	}
	for _, tt := range cases {
		t.Run(tt.name, func(t *testing.T) {
			h := &Home{timeFilter: session.TimeFilter3Days, groupScope: tt.scope, remoteSessions: map[string][]session.RemoteSessionInfo{"dev": tt.remotes}}
			if tt.archived {
				h.statusFilter = FilterModeArchived
				for _, inst := range tt.locals {
					inst.ArchivedAt = now
				}
			}
			h.groupTree = session.NewGroupTree(tt.locals)
			originalRemotes := append([]session.RemoteSessionInfo(nil), tt.remotes...)
			h.rebuildFlatItems()
			if h.timeFilter != tt.wantMode {
				t.Errorf("mode = %v, want %v", h.timeFilter, tt.wantMode)
			}
			var ids []string
			for _, item := range h.flatItems {
				if item.Type == session.ItemTypeSession && item.Session != nil {
					ids = append(ids, item.Session.ID)
				}
				if item.Type == session.ItemTypeRemoteSession && item.RemoteSession != nil {
					ids = append(ids, item.RemoteSession.ID)
				}
			}
			sort.Strings(ids)
			if !reflect.DeepEqual(ids, tt.wantIDs) {
				t.Errorf("visible session IDs = %v, want %v", ids, tt.wantIDs)
			}
			if !reflect.DeepEqual(h.remoteSessions["dev"], originalRemotes) {
				t.Error("filter mutated remote snapshot")
			}
		})
	}
}

func TestTimeFilterFleetPreservesCollapsedMatches(t *testing.T) {
	now := time.Now()
	for _, collapse := range []string{"local", "remotes/dev", "remotes/dev/work"} {
		t.Run(collapse, func(t *testing.T) {
			h := &Home{
				timeFilter: session.TimeFilter3Days,
				groupTree: session.NewGroupTree([]*session.Instance{
					{ID: "local-recent", Title: "recent", GroupPath: "local", LastAccessedAt: now},
					{ID: "local-other", Title: "other", GroupPath: "other", LastAccessedAt: now},
				}),
				remoteSessions: map[string][]session.RemoteSessionInfo{"dev": {
					{ID: "remote-recent", Group: "work", LastActivityAt: now.Format(time.RFC3339Nano)},
					{ID: "remote-old", Group: "old", LastActivityAt: now.AddDate(0, 0, -30).Format(time.RFC3339Nano)},
				}},
				remoteGroupsCollapsed: map[string]bool{collapse: true},
			}
			for _, group := range h.groupTree.GroupList {
				if group.Path == collapse {
					group.Expanded = false
				}
			}
			h.rebuildFlatItems()
			foundHeader := false
			for _, item := range h.flatItems {
				if item.Path == collapse && (item.Type == session.ItemTypeGroup || item.Type == session.ItemTypeRemoteGroup) {
					foundHeader = true
				}
				if collapse == "local" && item.Session != nil && item.Session.ID == "local-recent" {
					t.Error("collapsed local child visible")
				}
				if item.RemoteSession != nil && (item.RemoteSession.ID == "remote-old" || collapse != "local") {
					t.Errorf("filtered or collapsed remote child visible: %s", item.RemoteSession.ID)
				}
			}
			if !foundHeader {
				t.Error("matching collapsed header disappeared")
			}
			if h.timeFilter != session.TimeFilter3Days {
				t.Error("matching collapsed group caused fallback")
			}
		})
	}
}

func TestTimeFilterFleetCollapsedStatusMatchRemainsReachable(t *testing.T) {
	now := time.Now()
	h := &Home{
		statusFilter: session.StatusRunning,
		timeFilter:   session.TimeFilter3Days,
		groupTree: session.NewGroupTree([]*session.Instance{
			{ID: "old-running", Title: "old", GroupPath: "expanded", Status: session.StatusRunning, LastAccessedAt: now.AddDate(0, 0, -30)},
			{ID: "recent-running", Title: "recent", GroupPath: "collapsed", Status: session.StatusRunning, LastAccessedAt: now},
		}),
	}
	for _, group := range h.groupTree.GroupList {
		if group.Path == "collapsed" {
			group.Expanded = false
		}
	}
	h.rebuildFlatItems()
	if h.timeFilter != session.TimeFilter3Days || h.statusFilter != session.StatusRunning {
		t.Error("matching collapsed status group should retain both filters")
	}
	found := false
	for _, item := range h.flatItems {
		if item.Type == session.ItemTypeGroup && item.Path == "collapsed" {
			found = true
		}
		if item.Type == session.ItemTypeSession {
			t.Errorf("unexpected visible session: %s", item.Session.ID)
		}
	}
	if !found {
		t.Error("collapsed match cannot be reopened")
	}
}

func TestTimeFilterFleetPreservesRemoteSelectionAndConnector(t *testing.T) {
	now := time.Now()
	h := &Home{
		groupTree: session.NewGroupTree([]*session.Instance{{ID: "old", Title: "old", LastAccessedAt: now.AddDate(0, 0, -30)}}),
		remoteSessions: map[string][]session.RemoteSessionInfo{"dev": {
			{ID: "recent", Group: "work", LastActivityAt: now.Format(time.RFC3339Nano)},
			{ID: "old", Group: "work", LastActivityAt: now.AddDate(0, 0, -30).Format(time.RFC3339Nano)},
		}},
	}
	h.rebuildFlatItems()
	h.cursor = remoteSessionIndexByID(h, "recent")
	if h.cursor < 0 {
		t.Fatal("fixture lacks recent remote")
	}
	selected := h.captureSelectedItemIdentity()
	h.timeFilter = session.TimeFilter3Days
	h.rebuildFlatItemsPreservingSelection(selected)
	idx := remoteSessionIndexByID(h, "recent")
	if idx < 0 || h.cursor != idx {
		t.Fatalf("selection moved: cursor=%d recent=%d", h.cursor, idx)
	}
	if !h.flatItems[idx].IsLastInGroup {
		t.Error("surviving last remote retained a middle-row connector")
	}
}
