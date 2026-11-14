//go:build !windows

package main

import (
	"testing"

	"github.com/asheshgoplani/agent-deck/internal/session"
)

// With no AGENTDECK_INSTANCE_ID / AGENT_DECK_SESSION_ID, `inbox drain self`
// (and every other resolveSelfSessionID caller) falls back to the tmux session
// name. That name ends in a random suffix, not the session ID, so the session
// must be found by its whole tmux name.
func TestResolveSelfSessionID_TmuxNameOnly(t *testing.T) {
	cliInboxTestHome(t)
	t.Setenv("AGENTDECK_INSTANCE_ID", "")
	t.Setenv("AGENT_DECK_SESSION_ID", "")

	storage, err := session.NewStorageWithProfile("")
	if err != nil {
		t.Fatalf("storage: %v", err)
	}
	inst := session.NewInstance("worker", t.TempDir())
	other := session.NewInstance("other", t.TempDir())
	instances := []*session.Instance{other, inst}
	if err := storage.SaveWithGroups(instances, session.NewGroupTreeWithGroups(instances, nil)); err != nil {
		t.Fatalf("save: %v", err)
	}
	_ = storage.Close()

	name := inst.GetTmuxSession().Name
	fakeTmuxSessionName(t, name)

	got, err := resolveSelfSessionID()
	if err != nil || got != inst.ID {
		t.Fatalf("resolveSelfSessionID() inside tmux session %q = %q, %v; want %q", name, got, err, inst.ID)
	}
}

// Auto-parent: a session created from inside another agent-deck session with
// no identity env must still get the caller as its parent, found by the whole
// tmux name rather than by its random suffix.
func TestResolveAutoParent_TmuxNameOnly(t *testing.T) {
	t.Setenv("AGENTDECK_INSTANCE_ID", "")
	t.Setenv("AGENT_DECK_SESSION_ID", "")
	inst := session.NewInstance("worker", "/tmp/project")
	other := session.NewInstance("other", "/tmp/other")
	fakeTmuxSessionName(t, inst.GetTmuxSession().Name)

	got, unresolved := resolveAutoParentInstanceChecked([]*session.Instance{other, inst})
	if got != inst || unresolved != "" {
		t.Fatalf("resolveAutoParentInstanceChecked inside tmux session %q = %v, %q; want instance %s",
			inst.GetTmuxSession().Name, got, unresolved, inst.ID)
	}
}
