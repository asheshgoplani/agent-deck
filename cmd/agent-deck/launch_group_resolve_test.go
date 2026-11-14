package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// launch -g must resolve a group selector exactly like add -g
// (resolveGroupPathForAdd). Before the fix launch used the raw selector, so
// "launch -g backend" with only platform/backend stored created a new
// top-level "backend" group, and an ambiguous name was not refused.

// setupLaunchGroupResolveTest seeds platform/backend plus two groups named
// workers (team/workers, yasir/workers) in an isolated HOME, and puts a no-op
// tmux first on PATH so launch reaches its save without a tmux server.
func setupLaunchGroupResolveTest(t *testing.T) (home, project string) {
	t.Helper()
	if testing.Short() {
		t.Skip("subprocess CLI test skipped in short mode")
	}
	home = t.TempDir()
	project = filepath.Join(home, "project")
	if err := os.MkdirAll(project, 0o755); err != nil {
		t.Fatal(err)
	}
	binDir := t.TempDir()
	fakeTmux := "#!/bin/sh\ncase \" $* \" in\n  *' has-session '*) exit 1 ;;\nesac\nexit 0\n"
	if err := os.WriteFile(filepath.Join(binDir, "tmux"), []byte(fakeTmux), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", binDir+string(os.PathListSeparator)+os.Getenv("PATH"))

	for _, args := range [][]string{
		{"group", "create", "platform", "--json"},
		{"group", "create", "backend", "--parent", "platform", "--json"},
		{"group", "create", "team", "--json"},
		{"group", "create", "yasir", "--json"},
		{"group", "create", "workers", "--parent", "team", "--json"},
		{"group", "create", "workers", "--parent", "yasir", "--json"},
	} {
		if stdout, stderr, code := runAgentDeck(t, home, args...); code != 0 {
			t.Fatalf("%v failed (%d)\n%s\n%s", args, code, stdout, stderr)
		}
	}
	return home, project
}

func launchGroupPaths(t *testing.T, home string) []string {
	t.Helper()
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
	var paths []string
	for _, g := range resp.Groups {
		paths = append(paths, g.Path)
	}
	return paths
}

func launchedSessionGroup(t *testing.T, home, title string) string {
	t.Helper()
	stdout, stderr, code := runAgentDeck(t, home, "list", "--json")
	if code != 0 {
		t.Fatalf("list failed (%d)\n%s\n%s", code, stdout, stderr)
	}
	var sessions []struct {
		Title string `json:"title"`
		Group string `json:"group"`
	}
	if err := json.Unmarshal([]byte(stdout), &sessions); err != nil {
		t.Fatalf("unmarshal list: %v\n%s", err, stdout)
	}
	for _, s := range sessions {
		if s.Title == title {
			return s.Group
		}
	}
	t.Fatalf("session %q not listed:\n%s", title, stdout)
	return ""
}

func TestLaunch_GroupUniqueNestedNameResolvesLikeAdd(t *testing.T) {
	home, project := setupLaunchGroupResolveTest(t)

	stdout, stderr, code := runAgentDeck(t, home, "launch", project, "-c", "shell", "-t", "l1", "-g", "backend", "--no-parent", "--no-wait")
	if code != 0 {
		t.Fatalf("launch -g backend: exit=%d\nstdout: %s\nstderr: %s", code, stdout, stderr)
	}
	if got := launchedSessionGroup(t, home, "l1"); got != "platform/backend" {
		t.Fatalf("launch -g backend landed in %q, want platform/backend", got)
	}
	for _, p := range launchGroupPaths(t, home) {
		if p == "backend" {
			t.Fatalf("launch -g backend created a top-level backend group")
		}
	}
}

func TestLaunch_GroupAmbiguousNameFailsLikeAdd(t *testing.T) {
	home, project := setupLaunchGroupResolveTest(t)

	stdout, stderr, code := runAgentDeck(t, home, "launch", project, "-c", "shell", "-t", "l2", "-g", "workers", "--no-parent", "--no-wait")
	want := `group "workers" is ambiguous: team/workers, yasir/workers; use the full path`
	if code == 0 || !strings.Contains(stdout+stderr, want) {
		t.Fatalf("launch -g workers: exit=%d, want non-zero and %q\nstdout: %s\nstderr: %s", code, want, stdout, stderr)
	}
	for _, p := range launchGroupPaths(t, home) {
		if p == "workers" {
			t.Fatalf("ambiguous launch created a top-level workers group")
		}
	}
	stdout, stderr, code = runAgentDeck(t, home, "list", "--json")
	if code != 0 || strings.Contains(stdout, `"l2"`) {
		t.Fatalf("ambiguous launch registered a session (exit=%d)\n%s\n%s", code, stdout, stderr)
	}
}

// A name matching no group keeps add's behaviour: the group is created under
// the name given.
func TestLaunch_GroupNewNameCreatesGroupLikeAdd(t *testing.T) {
	home, project := setupLaunchGroupResolveTest(t)

	stdout, stderr, code := runAgentDeck(t, home, "launch", project, "-c", "shell", "-t", "l3", "-g", "brandnew", "--no-parent", "--no-wait")
	if code != 0 {
		t.Fatalf("launch -g brandnew: exit=%d\nstdout: %s\nstderr: %s", code, stdout, stderr)
	}
	if got := launchedSessionGroup(t, home, "l3"); got != "brandnew" {
		t.Fatalf("launch -g brandnew landed in %q, want brandnew", got)
	}
}
