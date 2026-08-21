package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

func TestIssue1992_ListJSONCarriesChildParentLinkage(t *testing.T) {
	if testing.Short() {
		t.Skip("subprocess CLI test skipped in short mode")
	}
	home := t.TempDir()
	parentPath := filepath.Join(home, "parent")
	childPath := filepath.Join(home, "child")
	for _, path := range []string{parentPath, childPath} {
		if err := os.MkdirAll(path, 0o755); err != nil {
			t.Fatal(err)
		}
	}

	parentOut, stderr, code := runAgentDeck(t, home, "add", parentPath, "--title", "parent-1992", "--no-parent", "--json")
	if code != 0 {
		t.Fatalf("add parent exit %d: %s", code, stderr)
	}
	var parent struct {
		ID string `json:"id"`
	}
	if err := json.Unmarshal([]byte(parentOut), &parent); err != nil {
		t.Fatalf("parse parent: %v\n%s", err, parentOut)
	}
	childOut, stderr, code := runAgentDeck(t, home, "add", childPath, "--title", "child-1992", "--parent", parent.ID, "--json")
	if code != 0 {
		t.Fatalf("add child exit %d: %s", code, stderr)
	}
	var child struct {
		ID string `json:"id"`
	}
	if err := json.Unmarshal([]byte(childOut), &child); err != nil {
		t.Fatalf("parse child: %v\n%s", err, childOut)
	}

	for _, tc := range []struct {
		name string
		args []string
	}{
		{name: "current profile", args: []string{"list", "--json"}},
		{name: "all profiles", args: []string{"list", "--all", "--json"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			listOut, stderr, code := runAgentDeck(t, home, tc.args...)
			if code != 0 {
				t.Fatalf("%v exit %d: %s", tc.args, code, stderr)
			}
			var rows []map[string]any
			if err := json.Unmarshal([]byte(listOut), &rows); err != nil {
				t.Fatalf("parse %v: %v\n%s", tc.args, err, listOut)
			}
			for _, row := range rows {
				if row["id"] != child.ID {
					continue
				}
				// Keep these assertions separate: the current-profile and
				// all-profile emitters each have independent assignments for
				// both fields, and every one is a regression guard.
				if got := row["parent_session_id"]; got != parent.ID {
					t.Fatalf("%s child parent_session_id = %#v, want %q; row=%#v", tc.name, got, parent.ID, row)
				}
				if got := row["parent_project_path"]; got != parentPath {
					t.Fatalf("%s child parent_project_path = %#v, want %q; row=%#v", tc.name, got, parentPath, row)
				}
				return
			}
			t.Fatalf("child %q missing from %v: %s", child.ID, tc.args, listOut)
		})
	}
}
