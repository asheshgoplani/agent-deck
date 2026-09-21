// WebMutator.SetGroupExpanded is the write half of TUI<->web collapse sync.
// Before it existed the web sidebar kept collapse state in localStorage only,
// so the two views drifted apart the moment either one collapsed anything.
//
// These cover the mutator's contract against a real GroupTree. Storage is nil
// throughout (the same no-persistence configuration the other group tests in
// this package use), which exercises everything up to the SaveGroupsOnly call.

package ui

import (
	"errors"
	"testing"

	"github.com/asheshgoplani/agent-deck/internal/session"
	"github.com/asheshgoplani/agent-deck/internal/web"
)

func homeWithGroups(t *testing.T, paths ...string) *Home {
	t.Helper()
	home := NewHome()
	home.width, home.height = 100, 30
	home.storage = nil

	instances := []*session.Instance{}
	home.instancesMu.Lock()
	home.instances = instances
	home.instancesMu.Unlock()
	home.groupTree = session.NewGroupTree(instances)
	for _, p := range paths {
		home.groupTree.CreateGroupPath(p)
	}
	return home
}

func TestWebMutatorSetGroupExpandedTogglesBothWays(t *testing.T) {
	home := homeWithGroups(t, "stride")
	m := &WebMutator{h: home}

	if err := m.SetGroupExpanded("stride", false); err != nil {
		t.Fatalf("collapse: %v", err)
	}
	if home.groupTree.Groups["stride"].Expanded {
		t.Error("group should be collapsed")
	}
	// The Expanded side-map has to move with the group, or Flatten and the
	// snapshot builder disagree about what is open.
	if home.groupTree.Expanded["stride"] {
		t.Error("tree Expanded map should agree that the group is collapsed")
	}

	if err := m.SetGroupExpanded("stride", true); err != nil {
		t.Fatalf("expand: %v", err)
	}
	if !home.groupTree.Groups["stride"].Expanded {
		t.Error("group should be expanded again")
	}
	if !home.groupTree.Expanded["stride"] {
		t.Error("tree Expanded map should agree that the group is open")
	}
}

// Collapsing a parent must NOT cascade. Visibility is derived by walking the
// ancestor chain at render time (GroupTree.ancestorsExpanded), so cascading
// here would make re-expanding the parent lose every child's own state.
func TestWebMutatorSetGroupExpandedDoesNotCascade(t *testing.T) {
	home := homeWithGroups(t, "stride", "stride/ws1")
	m := &WebMutator{h: home}

	if err := m.SetGroupExpanded("stride", false); err != nil {
		t.Fatalf("collapse parent: %v", err)
	}
	if !home.groupTree.Groups["stride/ws1"].Expanded {
		t.Error("collapsing a parent must leave the child's own flag alone")
	}
}

// A stale browser tab can PATCH a group the TUI already deleted. Silently
// succeeding would leave the sidebar asserting a collapse nothing stored.
func TestWebMutatorSetGroupExpandedUnknownGroup(t *testing.T) {
	home := homeWithGroups(t, "stride")
	m := &WebMutator{h: home}

	err := m.SetGroupExpanded("ghost", false)
	if !errors.Is(err, web.ErrGroupNotFound) {
		t.Fatalf("expected web.ErrGroupNotFound, got %v", err)
	}
}

// A subgroup implied by a session's path but never stored ("derived") is a
// legitimate collapse target — the TUI lets you collapse one, and the save
// path materializes it via its ensure:true storage snapshot.
func TestWebMutatorSetGroupExpandedAcceptsDerivedGroup(t *testing.T) {
	inst := session.NewInstanceWithGroup("api", t.TempDir(), "stride/ws1")
	instances := []*session.Instance{inst}

	home := NewHome()
	home.width, home.height = 100, 30
	home.storage = nil
	home.instancesMu.Lock()
	home.instances = instances
	home.instancesMu.Unlock()
	home.groupTree = session.NewGroupTree(instances)

	if _, ok := home.groupTree.Groups["stride"]; !ok {
		t.Fatal("precondition: parent 'stride' should have been derived from the session path")
	}

	m := &WebMutator{h: home}
	if err := m.SetGroupExpanded("stride", false); err != nil {
		t.Fatalf("collapse derived group: %v", err)
	}
	if home.groupTree.Groups["stride"].Expanded {
		t.Error("derived group should be collapsed")
	}
}

// The round trip that the feature actually promises: a collapse issued from the
// web lands in SQLite, so a TUI reading the same profile sees it.
//
// The tests above all run with storage = nil and so stop one line short of
// SaveGroupsOnly — deleting that line would not have failed any of them. This
// one drives the real headless path (the deployment behind `agent-deck web
// --no-tui`), where beginHeadlessTx also hydrates from storage first.
func TestWebMutatorSetGroupExpandedPersistsToStorage(t *testing.T) {
	home, storage := newHeadlessHomeForTest(t, "_test_group_expanded")
	home.groupTree.CreateGroupPath("stride/ws1")
	if err := storage.SaveGroupsOnly(home.groupTree); err != nil {
		t.Fatalf("seed groups: %v", err)
	}

	m := &WebMutator{h: home}
	if err := m.SetGroupExpanded("stride/ws1", false); err != nil {
		t.Fatalf("SetGroupExpanded: %v", err)
	}

	_, groups, err := storage.LoadWithGroups()
	if err != nil {
		t.Fatalf("LoadWithGroups: %v", err)
	}
	var found *session.GroupData
	for _, g := range groups {
		if g.Path == "stride/ws1" {
			found = g
			break
		}
	}
	if found == nil {
		t.Fatalf("group stride/ws1 missing from storage; got %d groups", len(groups))
	}
	if found.Expanded {
		t.Error("collapse did not reach storage — a TUI on this profile would still show it expanded")
	}

	// And back again, so the test cannot pass on a write that only ever
	// produces false.
	if err := m.SetGroupExpanded("stride/ws1", true); err != nil {
		t.Fatalf("re-expand: %v", err)
	}
	_, groups, err = storage.LoadWithGroups()
	if err != nil {
		t.Fatalf("LoadWithGroups after re-expand: %v", err)
	}
	for _, g := range groups {
		if g.Path == "stride/ws1" && !g.Expanded {
			t.Error("re-expand did not reach storage")
		}
	}
}

// A derived group has no row in state.db until something saves it. Its storage
// snapshot carries ensure:true, so collapsing one must MATERIALIZE the row —
// the same thing a TUI collapse does. The in-memory-only assertion in
// TestWebMutatorSetGroupExpandedAcceptsDerivedGroup cannot see this.
func TestWebMutatorSetGroupExpandedMaterializesDerivedGroup(t *testing.T) {
	home, storage := newHeadlessHomeForTest(t, "_test_group_expanded_derived")

	// Seed the SESSION only, never a groups row: "redwood" then exists purely
	// as a path segment. beginHeadlessTx re-hydrates from storage and rebuilds
	// the tree (home.go:5153-5157), so seeding storage — not the in-memory
	// tree — is what puts the derived group in front of the mutator.
	inst := session.NewInstanceWithGroup("api", t.TempDir(), "redwood/ws1")
	if err := storage.Save([]*session.Instance{inst}); err != nil {
		t.Fatalf("seed session: %v", err)
	}
	if err := home.HydrateInstancesFromStorage(); err != nil {
		t.Fatalf("hydrate: %v", err)
	}
	if _, ok := home.groupTree.Groups["redwood"]; !ok {
		t.Fatal("precondition: 'redwood' should have been derived from the session path")
	}
	for _, g := range mustLoadGroups(t, storage) {
		if g.Path == "redwood" {
			t.Fatal("precondition: 'redwood' should have no stored row yet")
		}
	}

	m := &WebMutator{h: home}
	if err := m.SetGroupExpanded("redwood", false); err != nil {
		t.Fatalf("collapse derived group: %v", err)
	}

	groups := mustLoadGroups(t, storage)
	for _, g := range groups {
		if g.Path == "redwood" {
			if g.Expanded {
				t.Error("derived group row was written but not collapsed")
			}
			return
		}
	}
	t.Fatalf("derived group 'redwood' was never materialized in storage; got %d groups", len(groups))
}

func mustLoadGroups(t *testing.T, storage *session.Storage) []*session.GroupData {
	t.Helper()
	_, groups, err := storage.LoadWithGroups()
	if err != nil {
		t.Fatalf("LoadWithGroups: %v", err)
	}
	return groups
}
