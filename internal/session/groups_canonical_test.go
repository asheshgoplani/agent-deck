package session

import "testing"

// A session launched into ~/DoozyX/Uniqcast/dispatcher derives the group
// "Uniqcast" straight off disk while the declared group is "uniqcast"; the
// mismatch used to fork a stray top-level twin holding that one session.
func TestCanonicalizeGroupPathSnapsCaseVariantOntoExistingGroup(t *testing.T) {
	existing := []string{"uniqcast", "uniqcast/dispatcher", "doozyx"}

	if got := CanonicalizeGroupPath(existing, "Uniqcast"); got != "uniqcast" {
		t.Errorf("expected Uniqcast to snap to uniqcast, got %q", got)
	}
	if got := CanonicalizeGroupPath(existing, "Uniqcast/Dispatcher"); got != "uniqcast/dispatcher" {
		t.Errorf("expected nested variant to snap, got %q", got)
	}
	if got := CanonicalizeGroupPath(existing, "DoozyX"); got != "doozyx" {
		t.Errorf("expected DoozyX to snap to doozyx, got %q", got)
	}
}

func TestCanonicalizeGroupPathMatchesSpacesAgainstHyphens(t *testing.T) {
	existing := []string{"smart-tv-web"}

	if got := CanonicalizeGroupPath(existing, "Smart TV Web"); got != "smart-tv-web" {
		t.Errorf("expected spaces to match hyphens, got %q", got)
	}
}

func TestCanonicalizeGroupPathKeepsExactAndUnknownPaths(t *testing.T) {
	existing := []string{"uniqcast", "Uniqcast"}

	if got := CanonicalizeGroupPath(existing, "Uniqcast"); got != "Uniqcast" {
		t.Errorf("exact match must win, got %q", got)
	}
	if got := CanonicalizeGroupPath(existing, "fjordbyte"); got != "fjordbyte" {
		t.Errorf("unknown path must pass through so a new group can be created, got %q", got)
	}
	if got := CanonicalizeGroupPath(nil, ""); got != "" {
		t.Errorf("empty path must pass through, got %q", got)
	}
}

func TestAddSessionFilesCaseVariantIntoExistingGroup(t *testing.T) {
	tree := NewGroupTree([]*Instance{{ID: "1", Title: "sibling", GroupPath: "uniqcast"}})

	inst := &Instance{ID: "2", Title: "conductor", GroupPath: "Uniqcast"}
	tree.AddSession(inst)

	if inst.GroupPath != "uniqcast" {
		t.Errorf("expected session to be filed under uniqcast, got %q", inst.GroupPath)
	}
	if _, stray := tree.Groups["Uniqcast"]; stray {
		t.Error("expected no stray Uniqcast group")
	}
	if got := len(tree.Groups["uniqcast"].Sessions); got != 2 {
		t.Errorf("expected 2 sessions in uniqcast, got %d", got)
	}
}

func TestAddSessionKeepsDeliberateCaseVariantGroup(t *testing.T) {
	tree := NewGroupTree([]*Instance{{ID: "1", Title: "sibling", GroupPath: "uniqcast"}})
	tree.CreateGroup("Uniqcast")

	inst := &Instance{ID: "2", Title: "conductor", GroupPath: "Uniqcast"}
	tree.AddSession(inst)

	if inst.GroupPath != "Uniqcast" {
		t.Errorf("an existing group must keep its own sessions, got %q", inst.GroupPath)
	}
}
