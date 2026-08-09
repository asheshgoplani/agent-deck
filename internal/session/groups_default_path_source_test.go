package session

import (
	"testing"
	"time"
)

// Issue #1879: DefaultPathForGroup conflates two different things behind one
// return value — an *explicit* per-group default_path and the *derived*
// most-recently-accessed-session heuristic. Callers implementing the documented
// "group default_path → global config default_path → cwd" chain short-circuit on
// any non-empty return, so once a group holds a single session the global
// default_path becomes unreachable.
//
// ExplicitDefaultPathForGroup and RecentSessionPathForGroup split the two so a
// caller can tell an authoritative override from a guess.

func TestExplicitDefaultPathForGroupIgnoresRecentSessionHeuristic(t *testing.T) {
	tree := NewGroupTree([]*Instance{})
	tree.AddSession(&Instance{
		ID:             "1",
		Title:          "first",
		GroupPath:      "dev",
		ProjectPath:    "/home/user/project-a",
		LastAccessedAt: time.Now(),
	})

	got, ok := tree.ExplicitDefaultPathForGroup("dev")
	if ok || got != "" {
		t.Fatalf("ExplicitDefaultPathForGroup(\"dev\") = (%q, %v), want (\"\", false): "+
			"the group has no explicit default_path, only a session in it", got, ok)
	}

	if got := tree.RecentSessionPathForGroup("dev"); got != "/home/user/project-a" {
		t.Fatalf("RecentSessionPathForGroup(\"dev\") = %q, want %q", got, "/home/user/project-a")
	}

	// The conflated accessor keeps its old behavior — the TUI new-session
	// dialog still prefills from the heuristic.
	if got := tree.DefaultPathForGroup("dev"); got != "/home/user/project-a" {
		t.Fatalf("DefaultPathForGroup(\"dev\") = %q, want the derived %q (unchanged behavior)",
			got, "/home/user/project-a")
	}
}

func TestExplicitDefaultPathForGroupReportsConfiguredOverride(t *testing.T) {
	tree := NewGroupTree([]*Instance{})
	tree.AddSession(&Instance{
		ID:             "1",
		Title:          "first",
		GroupPath:      "dev",
		ProjectPath:    "/home/user/project-a",
		LastAccessedAt: time.Now(),
	})
	if ok := tree.SetDefaultPathForGroup("dev", "/home/user/explicit"); !ok {
		t.Fatal("SetDefaultPathForGroup should return true for an existing group")
	}

	got, ok := tree.ExplicitDefaultPathForGroup("dev")
	if !ok || got != "/home/user/explicit" {
		t.Fatalf("ExplicitDefaultPathForGroup(\"dev\") = (%q, %v), want (%q, true)",
			got, ok, "/home/user/explicit")
	}

	// The heuristic is still reported separately, unaffected by the override.
	if got := tree.RecentSessionPathForGroup("dev"); got != "/home/user/project-a" {
		t.Fatalf("RecentSessionPathForGroup(\"dev\") = %q, want %q", got, "/home/user/project-a")
	}
}

func TestExplicitDefaultPathForGroupUnknownGroup(t *testing.T) {
	tree := NewGroupTree([]*Instance{})

	if got, ok := tree.ExplicitDefaultPathForGroup("nope"); ok || got != "" {
		t.Fatalf("ExplicitDefaultPathForGroup(\"nope\") = (%q, %v), want (\"\", false)", got, ok)
	}
	if got := tree.RecentSessionPathForGroup("nope"); got != "" {
		t.Fatalf("RecentSessionPathForGroup(\"nope\") = %q, want \"\"", got)
	}
}

// A group whose sessions have no ProjectPath yields neither an explicit nor a
// derived path, so the caller must fall all the way through to cwd.
func TestRecentSessionPathForGroupEmptyWhenSessionsHaveNoPath(t *testing.T) {
	tree := NewGroupTree([]*Instance{})
	tree.AddSession(&Instance{ID: "1", Title: "first", GroupPath: "dev", ProjectPath: ""})

	if got := tree.RecentSessionPathForGroup("dev"); got != "" {
		t.Fatalf("RecentSessionPathForGroup(\"dev\") = %q, want \"\"", got)
	}
}
