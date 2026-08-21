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

	listOut, stderr, code := runAgentDeck(t, home, "list", "--json")
	if code != 0 {
		t.Fatalf("list --json exit %d: %s", code, stderr)
	}
	var rows []map[string]any
	if err := json.Unmarshal([]byte(listOut), &rows); err != nil {
		t.Fatalf("parse list: %v\n%s", err, listOut)
	}
	for _, row := range rows {
		if row["id"] != child.ID {
			continue
		}
		if got := row["parent_session_id"]; got != parent.ID {
			t.Fatalf("child parent_session_id = %#v, want %q; row=%#v", got, parent.ID, row)
		}
		if got := row["parent_project_path"]; got != parentPath {
			t.Fatalf("child parent_project_path = %#v, want %q; row=%#v", got, parentPath, row)
		}
		return
	}
	t.Fatalf("child %q missing from list: %s", child.ID, listOut)
}
