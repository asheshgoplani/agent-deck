package main

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/asheshgoplani/agent-deck/internal/session"
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

// `launch` with a parent and no -g took the group from the project directory
// as found, so "My Proj" was stored next to the "My-Proj" that add and
// launch --no-parent make: two groups with one name again. With a parent the
// directory name follows the same path rule.
func TestLaunch_WithParentGroupFromDirectoryWithSpaceMakesOneGroup(t *testing.T) {
	if testing.Short() {
		t.Skip("subprocess CLI test skipped in short mode")
	}
	if _, err := exec.LookPath("tmux"); err != nil {
		t.Skip("tmux not available")
	}
	for _, tc := range []struct {
		name        string
		noParentToo bool // a launch --no-parent made My-Proj first
	}{
		{"parent first", false},
		{"after no-parent launch", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			home := t.TempDir()
			base := t.TempDir()
			parentDir := filepath.Join(base, "plain", "par")
			app1 := filepath.Join(base, "My Proj", "app1")
			app2 := filepath.Join(base, "My Proj", "app2")
			for _, d := range []string{parentDir, app1, app2} {
				if err := os.MkdirAll(d, 0o755); err != nil {
					t.Fatal(err)
				}
			}
			t.Cleanup(func() {
				for _, s := range []string{"kid1", "kid2"} {
					runAgentDeck(t, home, "session", "stop", s)
				}
			})
			if stdout, stderr, code := runAgentDeck(t, home, "add", "-t", "par", "-c", "shell", "--no-parent", parentDir); code != 0 {
				t.Fatalf("add parent failed (%d)\n%s\n%s", code, stdout, stderr)
			}
			if tc.noParentToo {
				if stdout, stderr, code := runAgentDeck(t, home, "launch", app1, "-t", "kid1", "-c", "shell", "--no-parent", "--no-wait", "--json"); code != 0 {
					t.Fatalf("launch --no-parent failed (%d)\n%s\n%s", code, stdout, stderr)
				}
			}
			stdout, stderr, code := runAgentDeck(t, home, "launch", app2, "-t", "kid2", "-c", "shell", "--parent", "par", "--no-wait", "--json")
			if code != 0 {
				t.Fatalf("launch --parent failed (%d)\n%s\n%s", code, stdout, stderr)
			}
			rows := groupRows(t, home)
			if name, ok := rows["My-Proj"]; !ok || name != "My Proj" {
				t.Errorf("groups = %v, want My-Proj named \"My Proj\"", rows)
			}
			if _, raw := rows["My Proj"]; raw {
				t.Errorf("groups = %v: launch with a parent made a second group at the raw directory name", rows)
			}
			if len(rows) != 2 {
				t.Errorf("groups = %v, want only plain and My-Proj", rows)
			}
		})
	}
}

// The startup-query capacity check (and the group launch then stores when a
// startup query is given) keyed on the raw directory name, which add and
// launch no longer store; it is the canonical path.
func TestStartupQueryCapacity_DirectoryGroupIsCanonical(t *testing.T) {
	_, _, profile := setupAddDefaultPathTest(t)
	project := filepath.Join(t.TempDir(), "My Proj", "app")
	if err := os.MkdirAll(project, 0o755); err != nil {
		t.Fatal(err)
	}
	var group string
	if err := validateStartupQueryCapacity(profile, "", "", project, true, false, true, &group); err != nil {
		t.Fatalf("capacity check: %v", err)
	}
	if group != "My-Proj" {
		t.Errorf("capacity check keyed on group %q, want the stored path My-Proj", group)
	}
}

// A nested selector whose first level matches two groups by case used to
// make a third group; it is the same ambiguity error as for a whole selector.
func TestResolveGroupPathForAdd_NestedLevelMatchingTwoGroupsIsAmbiguous(t *testing.T) {
	tree := session.NewGroupTreeWithGroups(nil, []*session.GroupData{
		{Name: "Big Org", Path: "Big-Org"},
		{Name: "BIG ORG", Path: "BIG-ORG"},
	})
	got, err := resolveGroupPathForAdd(tree, "big-org/Third")
	if err == nil {
		t.Fatalf("resolved to %q, want an ambiguity error", got)
	}
	for _, want := range []string{"ambiguous", "BIG-ORG", "Big-Org"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error %q does not name %q", err, want)
		}
	}
	// One case match still nests under it.
	single := session.NewGroupTreeWithGroups(nil, []*session.GroupData{{Name: "Big Org", Path: "Big-Org"}})
	if got, err := resolveGroupPathForAdd(single, "big org/Third Kid"); err != nil || got != "Big-Org/Third-Kid" {
		t.Errorf("resolveGroupPathForAdd = %q, %v; want Big-Org/Third-Kid", got, err)
	}
}
