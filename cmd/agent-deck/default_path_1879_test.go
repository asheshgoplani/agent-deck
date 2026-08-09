package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/asheshgoplani/agent-deck/internal/session"
)

// Issue #1879: with a group whose default_path is unset, `launch`/`add` must
// still reach the global config default_path. Before the fix,
// GroupTree.DefaultPathForGroup returned the most-recently-accessed session's
// project path for such a group, and both call sites treated that derived value
// as an explicit per-group override — so the global default_path was
// unreachable for any group that had ever hosted a session.
//
// Documented chain (`launch --help`): explicit arg → group default_path →
// global config default_path → cwd. The most-recent-session heuristic is kept,
// but demoted to after the global default_path.
//
// Reuses the env isolation helpers from add_test.go.

// seedGroupWithSession stores a group with an EMPTY default_path plus one
// session sitting in sessionPath, mimicking the issue's repro state.
func seedGroupWithSession(t *testing.T, profile, name, groupPath, sessionPath string) {
	t.Helper()

	storage, err := session.NewStorageWithProfile(profile)
	if err != nil {
		t.Fatalf("NewStorageWithProfile: %v", err)
	}
	inst := session.NewInstanceWithGroupAndTool("existing", sessionPath, groupPath, "claude")
	inst.LastAccessedAt = time.Now()
	instances := []*session.Instance{inst}

	groupTree := session.NewGroupTreeWithGroups(instances, []*session.GroupData{
		{Name: name, Path: groupPath, Expanded: true, DefaultPath: ""},
	})
	if err := storage.SaveWithGroups(instances, groupTree); err != nil {
		t.Fatalf("SaveWithGroups: %v", err)
	}
	if err := storage.Close(); err != nil {
		t.Fatalf("Close storage: %v", err)
	}
}

// addedSessionPathByTitle returns the ProjectPath of the stored session with
// the given title. Unlike onlyAddedSessionPath it tolerates pre-seeded
// sessions.
func addedSessionPathByTitle(t *testing.T, profile, title string) string {
	t.Helper()

	storage, err := session.NewStorageWithProfile(profile)
	if err != nil {
		t.Fatalf("NewStorageWithProfile: %v", err)
	}
	defer storage.Close()
	instances, _, err := storage.LoadWithGroups()
	if err != nil {
		t.Fatalf("LoadWithGroups: %v", err)
	}
	for _, inst := range instances {
		if inst.Title == title {
			return inst.ProjectPath
		}
	}
	t.Fatalf("no stored session titled %q (loaded %d sessions)", title, len(instances))
	return ""
}

// TestResolveLaunchPathEmptyGroupDefaultReachesGlobalDefaultPath is the direct
// #1879 repro for `launch`: an existing session in the group must not shadow the
// configured global default_path.
func TestResolveLaunchPathEmptyGroupDefaultReachesGlobalDefaultPath(t *testing.T) {
	home, _, profile := setupAddDefaultPathTest(t)
	globalDefault := filepath.Join(home, "global-home")
	otherProject := filepath.Join(home, "some-other-project")
	for _, dir := range []string{globalDefault, otherProject} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatalf("mkdir %s: %v", dir, err)
		}
	}
	writeAddUserConfig(t, home, `default_path = "`+globalDefault+`"`+"\n")
	seedGroupWithSession(t, profile, "MyGroup", "my-group", otherProject)

	got, err := resolveLaunchPath("", "my-group", profile)
	if err != nil {
		t.Fatalf("resolveLaunchPath: %v", err)
	}
	if got != globalDefault {
		t.Fatalf("issue #1879: pathless launch into a group with an empty default_path resolved to %q, "+
			"want the global config default_path %q", got, globalDefault)
	}
}

// TestResolveLaunchPathRecentSessionStillUsedWithoutGlobalDefault pins that the
// heuristic survives — it is demoted below the global default_path, not deleted.
func TestResolveLaunchPathRecentSessionStillUsedWithoutGlobalDefault(t *testing.T) {
	home, cwd, profile := setupAddDefaultPathTest(t)
	otherProject := filepath.Join(home, "some-other-project")
	if err := os.MkdirAll(otherProject, 0o755); err != nil {
		t.Fatalf("mkdir %s: %v", otherProject, err)
	}
	seedGroupWithSession(t, profile, "MyGroup", "my-group", otherProject)

	got, err := resolveLaunchPath("", "my-group", profile)
	if err != nil {
		t.Fatalf("resolveLaunchPath: %v", err)
	}
	if got != otherProject {
		t.Fatalf("with no global default_path configured, pathless launch resolved to %q, "+
			"want the group's most-recent-session path %q (cwd is %q)", got, otherProject, cwd)
	}
}

// TestResolveLaunchPathExplicitGroupDefaultStillWins guards the fix from
// over-correcting: an explicit per-group default_path must still beat the
// global one even when the group also holds sessions elsewhere.
func TestResolveLaunchPathExplicitGroupDefaultStillWins(t *testing.T) {
	home, _, profile := setupAddDefaultPathTest(t)
	groupDefault := filepath.Join(home, "group-home")
	globalDefault := filepath.Join(home, "global-home")
	otherProject := filepath.Join(home, "some-other-project")
	for _, dir := range []string{groupDefault, globalDefault, otherProject} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatalf("mkdir %s: %v", dir, err)
		}
	}
	writeAddUserConfig(t, home, `default_path = "`+globalDefault+`"`+"\n")

	storage, err := session.NewStorageWithProfile(profile)
	if err != nil {
		t.Fatalf("NewStorageWithProfile: %v", err)
	}
	inst := session.NewInstanceWithGroupAndTool("existing", otherProject, "my-group", "claude")
	inst.LastAccessedAt = time.Now()
	instances := []*session.Instance{inst}
	groupTree := session.NewGroupTreeWithGroups(instances, []*session.GroupData{
		{Name: "MyGroup", Path: "my-group", Expanded: true, DefaultPath: groupDefault},
	})
	if err := storage.SaveWithGroups(instances, groupTree); err != nil {
		t.Fatalf("SaveWithGroups: %v", err)
	}
	if err := storage.Close(); err != nil {
		t.Fatalf("Close storage: %v", err)
	}

	got, err := resolveLaunchPath("", "my-group", profile)
	if err != nil {
		t.Fatalf("resolveLaunchPath: %v", err)
	}
	if got != groupDefault {
		t.Fatalf("pathless launch resolved to %q, want the explicit group default_path %q", got, groupDefault)
	}
}

// TestHandleAddEmptyGroupDefaultReachesGlobalDefaultPath is the same repro for
// `add`, which carries its own copy of the chain in handleAdd.
func TestHandleAddEmptyGroupDefaultReachesGlobalDefaultPath(t *testing.T) {
	home, _, profile := setupAddDefaultPathTest(t)
	globalDefault := filepath.Join(home, "global-home")
	otherProject := filepath.Join(home, "some-other-project")
	for _, dir := range []string{globalDefault, otherProject} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatalf("mkdir %s: %v", dir, err)
		}
	}
	writeAddUserConfig(t, home, `default_path = "`+globalDefault+`"`+"\n")
	seedGroupWithSession(t, profile, "MyGroup", "my-group", otherProject)

	handleAdd(profile, []string{"--group", "my-group", "--no-parent", "--title", "pathless", "--quiet"})

	if got := addedSessionPathByTitle(t, profile, "pathless"); got != globalDefault {
		t.Fatalf("issue #1879: pathless add into a group with an empty default_path landed in %q, "+
			"want the global config default_path %q", got, globalDefault)
	}
}

// TestDescribeGroupDefaultPathDistinguishesDerivedFromExplicit pins the second
// half of #1879: `group show`/`create`/`update` must not present the
// most-recent-session guess as if it were the group's configured default_path.
func TestDescribeGroupDefaultPathDistinguishesDerivedFromExplicit(t *testing.T) {
	inst := session.NewInstanceWithGroupAndTool("existing", "/home/user/some-other-project", "my-group", "claude")
	inst.LastAccessedAt = time.Now()
	instances := []*session.Instance{inst}

	t.Run("derived", func(t *testing.T) {
		tree := session.NewGroupTreeWithGroups(instances, []*session.GroupData{
			{Name: "MyGroup", Path: "my-group", Expanded: true, DefaultPath: ""},
		})

		got := describeGroupDefaultPath(tree, "my-group")
		if got.Source != "recent_session" {
			t.Fatalf("Source = %q, want %q", got.Source, "recent_session")
		}
		if got.Effective != "/home/user/some-other-project" {
			t.Fatalf("Effective = %q, want %q", got.Effective, "/home/user/some-other-project")
		}
		if got.Explicit != "" {
			t.Fatalf("Explicit = %q, want \"\": no default_path is configured on this group", got.Explicit)
		}
		if !strings.Contains(got.Display(), "derived") {
			t.Fatalf("Display() = %q, want it to mark the value as derived", got.Display())
		}
	})

	t.Run("explicit", func(t *testing.T) {
		tree := session.NewGroupTreeWithGroups(instances, []*session.GroupData{
			{Name: "MyGroup", Path: "my-group", Expanded: true, DefaultPath: "/home/user/configured"},
		})

		got := describeGroupDefaultPath(tree, "my-group")
		if got.Source != "explicit" {
			t.Fatalf("Source = %q, want %q", got.Source, "explicit")
		}
		if got.Effective != "/home/user/configured" || got.Explicit != "/home/user/configured" {
			t.Fatalf("got Effective=%q Explicit=%q, want both %q", got.Effective, got.Explicit, "/home/user/configured")
		}
		if strings.Contains(got.Display(), "derived") {
			t.Fatalf("Display() = %q, must not annotate a configured default_path as derived", got.Display())
		}
	})

	t.Run("none", func(t *testing.T) {
		tree := session.NewGroupTreeWithGroups(nil, []*session.GroupData{
			{Name: "Empty", Path: "empty", Expanded: true},
		})

		got := describeGroupDefaultPath(tree, "empty")
		if got.Source != "none" || got.Effective != "" || got.Explicit != "" {
			t.Fatalf("got %+v, want an empty report with Source %q", got, "none")
		}
		if got.Display() != "(none)" {
			t.Fatalf("Display() = %q, want %q", got.Display(), "(none)")
		}
	})
}

// TestHandleAddRecentSessionStillUsedWithoutGlobalDefault mirrors the launch
// case: without a global default_path the heuristic remains the answer.
func TestHandleAddRecentSessionStillUsedWithoutGlobalDefault(t *testing.T) {
	home, _, profile := setupAddDefaultPathTest(t)
	otherProject := filepath.Join(home, "some-other-project")
	if err := os.MkdirAll(otherProject, 0o755); err != nil {
		t.Fatalf("mkdir %s: %v", otherProject, err)
	}
	seedGroupWithSession(t, profile, "MyGroup", "my-group", otherProject)

	handleAdd(profile, []string{"--group", "my-group", "--no-parent", "--title", "pathless", "--quiet"})

	if got := addedSessionPathByTitle(t, profile, "pathless"); got != otherProject {
		t.Fatalf("with no global default_path configured, pathless add landed in %q, "+
			"want the group's most-recent-session path %q", got, otherProject)
	}
}
