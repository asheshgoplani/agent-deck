package main

import (
	"encoding/json"
	"testing"
)

// groupListNode mirrors the recursive shape `group list --json` emits.
type groupListNode struct {
	Path     string          `json:"path"`
	Children []groupListNode `json:"children"`
}

// TestGroupList_JSONNestsBeyondOneLevel pins the producer side of the remote
// group list contract: the tree must be recursive, so an empty group three
// levels deep (work/archive/old) is emitted under its grandparent rather than
// dropped. The TUI's remote move dialog and Shift+Up/Down reorder at depth
// both read this tree over SSH and have no session-derived fallback for an
// empty group.
func TestGroupList_JSONNestsBeyondOneLevel(t *testing.T) {
	if testing.Short() {
		t.Skip("subprocess CLI test skipped in short mode")
	}
	home := t.TempDir()

	for _, args := range [][]string{
		{"group", "create", "work", "--json"},
		{"group", "create", "archive", "--parent", "work", "--json"},
		{"group", "create", "old", "--parent", "work/archive", "--json"},
		{"group", "create", "new", "--parent", "work/archive", "--json"},
	} {
		if stdout, stderr, code := runAgentDeck(t, home, args...); code != 0 {
			t.Fatalf("%v failed (%d)\n%s\n%s", args, code, stdout, stderr)
		}
	}

	stdout, stderr, code := runAgentDeck(t, home, "group", "list", "--json")
	if code != 0 {
		t.Fatalf("group list failed (%d)\n%s\n%s", code, stdout, stderr)
	}
	var resp struct {
		Groups []groupListNode `json:"groups"`
	}
	if err := json.Unmarshal([]byte(stdout), &resp); err != nil {
		t.Fatalf("unmarshal group list: %v\n%s", err, stdout)
	}

	var work *groupListNode
	for i := range resp.Groups {
		if resp.Groups[i].Path == "work" {
			work = &resp.Groups[i]
		}
		if resp.Groups[i].Path != "work" {
			t.Errorf("unexpected root group %q: nested groups must not be flattened to the root", resp.Groups[i].Path)
		}
	}
	if work == nil {
		t.Fatalf("root group work missing from:\n%s", stdout)
	}
	if len(work.Children) != 1 || work.Children[0].Path != "work/archive" {
		t.Fatalf("work.children = %+v, want exactly [work/archive]", work.Children)
	}
	archive := work.Children[0]
	got := make([]string, 0, len(archive.Children))
	for _, c := range archive.Children {
		got = append(got, c.Path)
	}
	if len(got) != 2 || got[0] != "work/archive/old" || got[1] != "work/archive/new" {
		t.Fatalf("work/archive.children = %v, want [work/archive/old work/archive/new] (depth 3, creation order)", got)
	}
}
