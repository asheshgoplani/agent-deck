package main

import (
	"reflect"
	"testing"
)

// TestCLI_VerbAliases_Accepted_RegressionFor974 covers the three CLI ergonomic
// rejections reported in issue #974:
//
//  1. `agent-deck session update <id> --no-parent`  — verb `update` rejected
//  2. `agent-deck group remove <name>`              — verb `remove` rejected
//  3. `agent-deck launch <path> ... -parent <pid>`  — value swallowed by path
//
// Each row asserts the user-facing parsing of the rejected form now succeeds
// and routes to the correct canonical operation.
func TestCLI_VerbAliases_Accepted_RegressionFor974(t *testing.T) {
	t.Run("session_update_with_no_parent_routes_to_unset_parent", func(t *testing.T) {
		canonical, newArgs := resolveSessionUpdateAlias([]string{"sess-abc", "--no-parent"})
		if canonical != "unset-parent" {
			t.Errorf("session update --no-parent: expected canonical=unset-parent, got %q", canonical)
		}
		if !reflect.DeepEqual(newArgs, []string{"sess-abc"}) {
			t.Errorf("session update --no-parent: expected args=[sess-abc], got %v", newArgs)
		}
	})

	t.Run("session_update_with_parent_value_routes_to_set_parent", func(t *testing.T) {
		canonical, newArgs := resolveSessionUpdateAlias([]string{"child", "--parent", "papa"})
		if canonical != "set-parent" {
			t.Errorf("session update --parent: expected canonical=set-parent, got %q", canonical)
		}
		if !reflect.DeepEqual(newArgs, []string{"child", "papa"}) {
			t.Errorf("session update --parent: expected [child papa], got %v", newArgs)
		}
	})

	t.Run("session_update_with_parent_equals_value_routes_to_set_parent", func(t *testing.T) {
		canonical, newArgs := resolveSessionUpdateAlias([]string{"child", "--parent=papa"})
		if canonical != "set-parent" {
			t.Errorf("session update --parent=papa: expected canonical=set-parent, got %q", canonical)
		}
		if !reflect.DeepEqual(newArgs, []string{"child", "papa"}) {
			t.Errorf("session update --parent=papa: expected [child papa], got %v", newArgs)
		}
	})

	t.Run("group_remove_aliases_to_delete", func(t *testing.T) {
		canonical, ok := groupVerbCanonical("remove")
		if !ok {
			t.Fatalf("group remove: expected verb to be recognized")
		}
		if canonical != "delete" {
			t.Errorf("group remove: expected canonical=delete, got %q", canonical)
		}
	})

	// Sanity: ensure the existing aliases still resolve through the same helper,
	// so the new `remove` row does not regress them.
	t.Run("group_rm_still_aliases_to_delete", func(t *testing.T) {
		canonical, ok := groupVerbCanonical("rm")
		if !ok || canonical != "delete" {
			t.Errorf("group rm: expected (delete, true), got (%q, %v)", canonical, ok)
		}
	})

	t.Run("launch_single_dash_parent_keeps_value_paired", func(t *testing.T) {
		in := []string{"/some/path", "-t", "X", "-c", "claude", "-parent", "parent-id"}
		got := reorderArgsForFlagParsing(in)

		// After reorder, `-parent` must be immediately followed by its value
		// (`parent-id`), not by the path. Otherwise downstream parsing pairs
		// `-parent` with `/some/path` and treats `parent-id` as the path.
		idx := -1
		for i, a := range got {
			if a == "-parent" {
				idx = i
				break
			}
		}
		if idx == -1 {
			t.Fatalf("launch -parent: flag missing from reordered args: %v", got)
		}
		if idx+1 >= len(got) {
			t.Fatalf("launch -parent: no following arg in %v", got)
		}
		if got[idx+1] != "parent-id" {
			t.Errorf("launch -parent: expected next arg=parent-id, got %q (full=%v)", got[idx+1], got)
		}
	})

	// Sanity: the canonical double-dash form must still pair correctly.
	t.Run("launch_double_dash_parent_keeps_value_paired", func(t *testing.T) {
		in := []string{"/some/path", "-t", "X", "-c", "claude", "--parent", "parent-id"}
		got := reorderArgsForFlagParsing(in)
		idx := -1
		for i, a := range got {
			if a == "--parent" {
				idx = i
				break
			}
		}
		if idx == -1 || idx+1 >= len(got) || got[idx+1] != "parent-id" {
			t.Errorf("launch --parent: expected value=parent-id immediately after flag, got %v", got)
		}
	})
}
