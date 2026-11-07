package core

import (
	"testing"

	"github.com/asheshgoplani/agent-deck/internal/session"
)

// group.list counts every group's whole subtree, so a session is counted
// once per ancestor. It used to refresh its status at each count: three
// full status refreshes for a session in deck/ashesh/archive on every
// `group list --json` (which a remote TUI polls). Each session is refreshed
// once per listing, and the counts stay recursive.
func TestGroupListRefreshesEachSessionOnce(t *testing.T) {
	instances := []*session.Instance{
		session.NewInstanceWithGroup("top", "/tmp/top", "deck"),
		session.NewInstanceWithGroup("mid", "/tmp/mid", "deck/ashesh"),
		session.NewInstanceWithGroup("deep-1", "/tmp/deep1", "deck/ashesh/archive"),
		session.NewInstanceWithGroup("deep-2", "/tmp/deep2", "deck/ashesh/archive"),
		session.NewInstanceWithGroup("other", "/tmp/other", "deck/yasir/archive"),
	}
	tree := session.NewGroupTreeWithGroups(instances, nil)

	calls := map[*session.Instance]int{}
	orig := groupListRefreshStatus
	t.Cleanup(func() { groupListRefreshStatus = orig })
	groupListRefreshStatus = func(inst *session.Instance) {
		calls[inst]++
		inst.Status = session.StatusWaiting
	}

	out := groupListFromTree(tree)
	for _, inst := range instances {
		if got := calls[inst]; got != 1 {
			t.Errorf("session %q (group %q) refreshed %d times, want 1", inst.Title, inst.GroupPath, got)
		}
	}
	want := map[string]int{
		"deck": 5, "deck/ashesh": 3, "deck/ashesh/archive": 2, "deck/yasir": 1, "deck/yasir/archive": 1,
	}
	for _, item := range out.Flat {
		if n, ok := want[item.Path]; ok && item.Status.Waiting != n {
			t.Errorf("group %q waiting = %d, want %d", item.Path, item.Status.Waiting, n)
		}
	}
	if out.TotalSessions != len(instances) {
		t.Errorf("total_sessions = %d, want %d", out.TotalSessions, len(instances))
	}
}
