package ui

import (
	"testing"

	"github.com/asheshgoplani/agent-deck/internal/session"
)

// reload2933Home builds a Home mid-reload with a group "g" holding orders 0..2
// and another group "other" whose higher orders must not leak into "g".
func reload2933Home(t *testing.T) *Home {
	t.Helper()
	insts := []*session.Instance{
		{ID: "g0", Title: "g0", Tool: "shell", GroupPath: "g", Order: 0, Status: session.StatusStopped},
		{ID: "g1", Title: "g1", Tool: "shell", GroupPath: "g", Order: 1, Status: session.StatusStopped},
		{ID: "g2", Title: "g2", Tool: "shell", GroupPath: "g", Order: 2, Status: session.StatusStopped},
		{ID: "o9", Title: "o9", Tool: "shell", GroupPath: "other", Order: 9, Status: session.StatusStopped},
	}
	h := newTestHomeWithItems(200, 50, nil)
	h.instances = insts
	h.instanceByID = map[string]*session.Instance{}
	for _, inst := range insts {
		h.instanceByID[inst.ID] = inst
	}
	h.groupTree = session.NewGroupTree(h.instances)
	h.reloadMu.Lock()
	h.isReloading = true
	h.reloadMu.Unlock()
	return h
}

// Issue #2933: a session created while a reload is in progress was saved with
// sort_order 0 and landed mid-group; it must get max(Order in group)+1.
func TestIssue2933_SessionCreatedDuringReloadGoesLast(t *testing.T) {
	h := reload2933Home(t)
	inst := &session.Instance{ID: "new", Title: "new", Tool: "shell", GroupPath: "g", Status: session.StatusStopped}
	_, _ = h.Update(sessionCreatedMsg{instance: inst})
	if inst.Order != 3 {
		t.Fatalf("created during reload: got Order %d, want 3 (last in group g)", inst.Order)
	}
}

// Issue #2933: same for a fork that completes while a reload is in progress.
func TestIssue2933_SessionForkedDuringReloadGoesLast(t *testing.T) {
	h := reload2933Home(t)
	inst := &session.Instance{ID: "fork", Title: "fork", Tool: "shell", GroupPath: "g", Status: session.StatusStopped}
	_, _ = h.Update(sessionForkedMsg{instance: inst, sourceID: "g1"})
	if inst.Order != 3 {
		t.Fatalf("forked during reload: got Order %d, want 3 (last in group g)", inst.Order)
	}
}
