package core

import (
	"testing"
	"time"

	"github.com/asheshgoplani/agent-deck/internal/session"
)

// group.list leaves archived sessions out of every count, the rule #3127
// applied to `status`, the TUI header pill, the group preview and remote
// rows. Archived rows from before #3127 can keep a stored `error`, so a raw
// count reported each one as a crash. A group whose sessions are all
// archived is still listed, with a count of 0, like an empty group.
func TestGroupListLeavesArchivedOut(t *testing.T) {
	type sess struct {
		group    string
		status   session.Status
		archived bool
	}
	type want struct {
		count  int
		status *GroupStatus // nil: no status object (count 0)
	}
	cases := []struct {
		name     string
		sessions []sess
		groups   map[string]want
		total    int
	}{
		{
			name: "old archived error row next to live rows",
			sessions: []sess{
				{"ops", session.StatusRunning, false},
				{"ops", session.StatusWaiting, false},
				{"ops", session.StatusError, true},
			},
			groups: map[string]want{
				"ops": {2, &GroupStatus{Running: 1, Waiting: 1}},
			},
			total: 2,
		},
		{
			name: "every session archived",
			sessions: []sess{
				{"old", session.StatusError, true},
				{"old", session.StatusStopped, true},
			},
			groups: map[string]want{
				"old": {0, nil},
			},
			total: 0,
		},
		{
			name: "archived rows in a subgroup stay out of the parent",
			sessions: []sess{
				{"deck", session.StatusIdle, false},
				{"deck/workers", session.StatusError, false},
				{"deck/workers", session.StatusError, true},
				{"deck/workers/done", session.StatusRunning, true},
			},
			groups: map[string]want{
				"deck":              {2, &GroupStatus{Idle: 1, Error: 1}},
				"deck/workers":      {1, &GroupStatus{Error: 1}},
				"deck/workers/done": {0, nil},
			},
			total: 2,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var instances []*session.Instance
			archived := map[*session.Instance]bool{}
			for i, s := range tc.sessions {
				inst := session.NewInstanceWithGroup("s"+string(rune('a'+i)), "/tmp/s", s.group)
				inst.Status = s.status
				if s.archived {
					inst.SetArchivedAtThreadSafe(time.Now())
					archived[inst] = true
				}
				instances = append(instances, inst)
			}
			tree := session.NewGroupTreeWithGroups(instances, nil)

			orig := groupListRefreshStatus
			t.Cleanup(func() { groupListRefreshStatus = orig })
			groupListRefreshStatus = func(inst *session.Instance) {
				if archived[inst] {
					t.Errorf("archived session %q refreshed; it is not counted", inst.Title)
				}
			}

			out := groupListFromTree(tree)
			if out.TotalSessions != tc.total {
				t.Errorf("total_sessions = %d, want %d", out.TotalSessions, tc.total)
			}

			nodes := map[string]GroupNode{}
			var walk func([]GroupNode)
			walk = func(ns []GroupNode) {
				for _, n := range ns {
					nodes[n.Path] = n
					walk(n.Children)
				}
			}
			walk(out.Groups)
			flat := map[string]GroupItem{}
			for _, item := range out.Flat {
				flat[item.Path] = item
			}

			for path, w := range tc.groups {
				node, ok := nodes[path]
				if !ok {
					t.Fatalf("group %q missing from the tree; a group of archived sessions is still listed", path)
				}
				if node.SessionCount != w.count {
					t.Errorf("group %q session_count = %d, want %d", path, node.SessionCount, w.count)
				}
				switch {
				case w.status == nil && node.Status != nil:
					t.Errorf("group %q status = %+v, want none (no sessions counted)", path, *node.Status)
				case w.status != nil && node.Status == nil:
					t.Errorf("group %q status missing, want %+v", path, *w.status)
				case w.status != nil && *node.Status != *w.status:
					t.Errorf("group %q status = %+v, want %+v", path, *node.Status, *w.status)
				}
				// The text table reads Flat.
				if flat[path].SessionCount != w.count {
					t.Errorf("group %q flat count = %d, want %d", path, flat[path].SessionCount, w.count)
				}
				wantFlat := GroupStatus{}
				if w.status != nil {
					wantFlat = *w.status
				}
				if flat[path].Status != wantFlat {
					t.Errorf("group %q flat status = %+v, want %+v", path, flat[path].Status, wantFlat)
				}
			}
		})
	}
}
