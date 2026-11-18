package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

// groupRows runs `group list --json` and returns every group, flattened.
func groupRows(t *testing.T, home string) map[string]string {
	t.Helper()
	stdout, stderr, code := runAgentDeck(t, home, "group", "list", "--json")
	if code != 0 {
		t.Fatalf("group list failed (%d)\n%s\n%s", code, stdout, stderr)
	}
	type node struct {
		Name     string `json:"name"`
		Path     string `json:"path"`
		Children []node `json:"children"`
	}
	var resp struct {
		Groups []node `json:"groups"`
	}
	if err := json.Unmarshal([]byte(stdout), &resp); err != nil {
		t.Fatalf("unmarshal group list: %v\n%s", err, stdout)
	}
	rows := map[string]string{}
	var walk func([]node)
	walk = func(nodes []node) {
		for _, n := range nodes {
			rows[n.Path] = n.Name
			walk(n.Children)
		}
	}
	walk(resp.Groups)
	return rows
}

// `add -g "My Team"` used to store the session under the path "My Team" and
// then create a second, empty group "My-Team": two groups with one name. A new
// group named on `add` gets the path `group create` gives it, and the session
// is in that group.
func TestAdd_GroupNameWithSpaceMakesOneGroup(t *testing.T) {
	if testing.Short() {
		t.Skip("subprocess CLI test skipped in short mode")
	}
	for _, tc := range []struct{ name, selector, wantPath, wantName string }{
		{"root", "My Team", "My-Team", "My Team"},
		{"nested", "Big Org/My Team", "Big-Org/My-Team", "My Team"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			home := t.TempDir()
			project := t.TempDir()
			stdout, stderr, code := runAgentDeck(t, home, "add", "-t", "one", "-c", "shell", "-g", tc.selector, "--no-parent", "--json", project)
			if code != 0 {
				t.Fatalf("add failed (%d)\n%s\n%s", code, stdout, stderr)
			}
			var added struct {
				Group string `json:"group"`
			}
			if err := json.Unmarshal([]byte(stdout), &added); err != nil {
				t.Fatalf("unmarshal add: %v\n%s", err, stdout)
			}
			if added.Group != tc.wantPath {
				t.Errorf("add reported group %q, want %q", added.Group, tc.wantPath)
			}
			rows := groupRows(t, home)
			if name, ok := rows[tc.wantPath]; !ok || name != tc.wantName {
				t.Errorf("groups = %v, want %q named %q", rows, tc.wantPath, tc.wantName)
			}
			if _, dup := rows[tc.selector]; dup {
				t.Errorf("groups = %v: the typed name %q is a second group", rows, tc.selector)
			}
			// A second add with the same typed name lands in the same group.
			if stdout, stderr, code := runAgentDeck(t, home, "add", "-t", "two", "-c", "shell", "-g", tc.selector, "--no-parent", "--json", project); code != 0 {
				t.Fatalf("second add failed (%d)\n%s\n%s", code, stdout, stderr)
			}
			if after := groupRows(t, home); len(after) != len(rows) {
				t.Errorf("second add changed the groups: %v -> %v", rows, after)
			}
		})
	}
}

// With no -g the group comes from the project's parent directory. A directory
// name with a space made the same two groups; it follows the same path rule.
func TestAdd_GroupFromDirectoryWithSpaceMakesOneGroup(t *testing.T) {
	if testing.Short() {
		t.Skip("subprocess CLI test skipped in short mode")
	}
	home := t.TempDir()
	project := filepath.Join(t.TempDir(), "My Proj", "app")
	if err := os.MkdirAll(project, 0o755); err != nil {
		t.Fatal(err)
	}
	for _, title := range []string{"p1", "p2"} {
		if stdout, stderr, code := runAgentDeck(t, home, "add", "-t", title, "-c", "shell", "--no-parent", project); code != 0 {
			t.Fatalf("add %s failed (%d)\n%s\n%s", title, code, stdout, stderr)
		}
	}
	rows := groupRows(t, home)
	if name, ok := rows["My-Proj"]; !ok || name != "My Proj" || len(rows) != 1 {
		t.Errorf("groups = %v, want only My-Proj named \"My Proj\"", rows)
	}
}

// A nested name whose parent exists in another case lands under that parent
// instead of making a second root group.
func TestAdd_NestedGroupNameFindsItsParentInAnotherCase(t *testing.T) {
	if testing.Short() {
		t.Skip("subprocess CLI test skipped in short mode")
	}
	home := t.TempDir()
	project := t.TempDir()
	runGroupCreate(t, home, "Big Org")
	if stdout, stderr, code := runAgentDeck(t, home, "add", "-t", "one", "-c", "shell", "-g", "big org/Second Kid", "--no-parent", project); code != 0 {
		t.Fatalf("add failed (%d)\n%s\n%s", code, stdout, stderr)
	}
	rows := groupRows(t, home)
	if name, ok := rows["Big-Org/Second-Kid"]; !ok || name != "Second Kid" || len(rows) != 2 {
		t.Errorf("groups = %v, want Big-Org and Big-Org/Second-Kid named \"Second Kid\"", rows)
	}
}

// `session move --group` to a group that does not exist yet makes the group
// `add -g` makes for the same name: nested on "/", each level with its typed
// name. (A nested name used to be flattened to one root group "Org-New-Team".)
func TestSessionMove_NewGroupFollowsTheAddPathRule(t *testing.T) {
	if testing.Short() {
		t.Skip("subprocess CLI test skipped in short mode")
	}
	home := t.TempDir()
	project := t.TempDir()
	if stdout, stderr, code := runAgentDeck(t, home, "add", "-t", "one", "-c", "shell", "-g", "base", "--no-parent", project); code != 0 {
		t.Fatalf("add failed (%d)\n%s\n%s", code, stdout, stderr)
	}
	if stdout, stderr, code := runAgentDeck(t, home, "session", "move", "one", t.TempDir(), "--group", "New Team", "--no-restart"); code != 0 {
		t.Fatalf("session move failed (%d)\n%s\n%s", code, stdout, stderr)
	}
	if stdout, stderr, code := runAgentDeck(t, home, "session", "move", "one", t.TempDir(), "--group", "Org/New Team", "--no-restart"); code != 0 {
		t.Fatalf("session move failed (%d)\n%s\n%s", code, stdout, stderr)
	}
	rows := groupRows(t, home)
	want := map[string]string{"base": "base", "New-Team": "New Team", "Org": "Org", "Org/New-Team": "New Team"}
	if len(rows) != len(want) {
		t.Errorf("groups = %v, want %v", rows, want)
	}
	for path, name := range want {
		if rows[path] != name {
			t.Errorf("group %q is named %q, want %q (groups = %v)", path, rows[path], name, rows)
		}
	}
}
