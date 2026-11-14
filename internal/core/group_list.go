package core

import (
	"context"
	"strings"

	"github.com/asheshgoplani/agent-deck/internal/session"
)

// GroupListIn is the input of group.list.
type GroupListIn struct {
	Profile string `json:"profile" doc:"Profile whose groups to list"`
}

// GroupListOut is the output of group.list. Groups is the tree (each group
// carries its direct children); Flat is every group in display order with its
// depth, for renderers that draw the tree themselves.
//
// The legacy `group list --json` marshalled a map, so its keys came out
// sorted: keep these JSON fields in alphabetical order.
type GroupListOut struct {
	Groups        []GroupNode `json:"groups"`
	TotalGroups   int         `json:"total_groups"`
	TotalSessions int         `json:"total_sessions"`
	Flat          []GroupItem `json:"-"`
}

// GroupNode is one group of the tree.
type GroupNode struct {
	Name         string       `json:"name"`
	Path         string       `json:"path"`
	SessionCount int          `json:"session_count" doc:"Sessions in this group and its subgroups"`
	Status       *GroupStatus `json:"status,omitempty" doc:"Status counts; absent for an empty group"`
	Children     []GroupNode  `json:"children,omitempty"`
}

// GroupStatus counts sessions by status, recursively.
type GroupStatus struct {
	Running int `json:"running"`
	Waiting int `json:"waiting"`
	Idle    int `json:"idle"`
	Error   int `json:"error"`
	Stopped int `json:"stopped"`
}

// GroupItem is one group of GroupListOut.Flat.
type GroupItem struct {
	Name         string
	Path         string
	Level        int
	SessionCount int
	Status       GroupStatus
}

func groupList(ctx context.Context, in GroupListIn) (GroupListOut, error) {
	d, err := loadSessionData(in.Profile)
	if err != nil {
		return GroupListOut{}, err
	}
	// Warm the pane-title cache and hook statuses once so the counts match
	// the TUI and /api/menu (#610).
	session.RefreshInstancesForCLIStatus(d.instances)
	return groupListFromTree(session.NewGroupTreeWithGroups(d.instances, d.groups)), nil
}

// groupListFromTree builds group.list's output from a loaded tree,
// refreshing the sessions' statuses for the counts.
func groupListFromTree(tree *session.GroupTree) GroupListOut {
	refreshTreeStatuses(tree)
	out := GroupListOut{
		Groups:        []GroupNode{},
		TotalGroups:   len(tree.Groups),
		TotalSessions: tree.SessionCount(),
	}
	nodes := make(map[string]GroupNode, len(tree.GroupList))
	for _, g := range tree.GroupList {
		count := tree.SessionCountForGroup(g.Path)
		status := groupStatus(tree, g.Path)
		node := GroupNode{Name: g.Name, Path: g.Path, SessionCount: count}
		if count > 0 {
			node.Status = &status
		}
		nodes[g.Path] = node
		out.Flat = append(out.Flat, GroupItem{
			Name: g.Name, Path: g.Path, Level: session.GetGroupLevel(g.Path),
			SessionCount: count, Status: status,
		})
	}

	// Each group carries its direct children (one level deeper, same prefix);
	// GroupList order is parent before children, siblings by persisted order.
	var subtree func(g *session.Group) GroupNode
	subtree = func(g *session.Group) GroupNode {
		node := nodes[g.Path]
		childLevel := session.GetGroupLevel(g.Path) + 1
		for _, child := range tree.GroupList {
			if strings.HasPrefix(child.Path, g.Path+"/") && session.GetGroupLevel(child.Path) == childLevel {
				node.Children = append(node.Children, subtree(child))
			}
		}
		return node
	}
	for _, g := range tree.GroupList {
		if session.GetGroupLevel(g.Path) == 0 {
			out.Groups = append(out.Groups, subtree(g))
		}
	}
	return out
}

// groupListRefreshStatus refreshes one session's status for the counts; a
// variable so tests can count the refreshes.
var groupListRefreshStatus = func(inst *session.Instance) { _ = inst.UpdateStatus() }

// refreshTreeStatuses refreshes every session of the tree once. A session is
// counted once per ancestor group; refreshing it at each count cost one full
// status refresh per level. GroupList gives a stable order; Groups (what the
// counts read) catches any group GroupList lacks.
func refreshTreeStatuses(tree *session.GroupTree) {
	seen := make(map[*session.Instance]struct{})
	refresh := func(g *session.Group) {
		for _, sess := range g.Sessions {
			if _, ok := seen[sess]; !ok {
				seen[sess] = struct{}{}
				groupListRefreshStatus(sess)
			}
		}
	}
	for _, g := range tree.GroupList {
		refresh(g)
	}
	for _, g := range tree.Groups {
		refresh(g)
	}
}

// groupStatus counts every session in path and its subgroups (statuses
// already refreshed by refreshTreeStatuses).
func groupStatus(tree *session.GroupTree, path string) GroupStatus {
	var st GroupStatus
	for p, sub := range tree.Groups {
		if p != path && !strings.HasPrefix(p, path+"/") {
			continue
		}
		for _, sess := range sub.Sessions {
			switch sess.Status {
			case session.StatusRunning:
				st.Running++
			case session.StatusWaiting:
				st.Waiting++
			case session.StatusIdle:
				st.Idle++
			case session.StatusError:
				st.Error++
			case session.StatusStopped:
				st.Stopped++
			}
		}
	}
	return st
}
