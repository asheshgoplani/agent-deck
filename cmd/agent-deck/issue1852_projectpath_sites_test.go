package main

import (
	"testing"

	"github.com/asheshgoplani/agent-deck/internal/session"
)

// Regression tests for https://github.com/asheshgoplani/agent-deck/issues/1852.
//
// Five sites identified a session by comparing Instance.ProjectPath, which for
// an --ssh session is only a local placeholder defaulting to the controller's
// working directory. Each of them could match the wrong session or refuse a
// right one.

// --- Sites 2 and 3: worktree occupancy ---------------------------------------

// TestLocalSessionPaths_ExcludesRemotePlaceholder pins the harmful direction
// called out in the issue: in the cleanup path a genuinely orphaned worktree was
// skipped as in-use because a remote session's placeholder equalled it.
func TestLocalSessionPaths_ExcludesRemotePlaceholder(t *testing.T) {
	orphan := "/home/dev/repo-worktrees/feature-x"

	remote := sshInstance("aaaaaaaaaaaaaaaa", "remote", "alice@host-a", "/srv/app-a")
	remote.ProjectPath = orphan // registered from inside the orphaned worktree dir

	paths := localSessionPaths([]*session.Instance{remote})
	if paths[orphan] {
		t.Fatalf("orphaned worktree %s counted as occupied by a remote session's placeholder — cleanup would never report it", orphan)
	}
}

// TestLocalSessionPaths_KeepsLocalAndWorktreePaths guards the useful half.
func TestLocalSessionPaths_KeepsLocalAndWorktreePaths(t *testing.T) {
	instances := []*session.Instance{
		{ID: "a", Title: "local", ProjectPath: "/home/dev/repo"},
		{ID: "b", Title: "wt", ProjectPath: "/home/dev/repo", WorktreePath: "/home/dev/repo-worktrees/feature-y"},
	}

	paths := localSessionPaths(instances)
	for _, want := range []string{"/home/dev/repo", "/home/dev/repo-worktrees/feature-y"} {
		if !paths[want] {
			t.Errorf("localSessionPaths dropped occupied path %s", want)
		}
	}
}

// TestLocalSessionPaths_RemoteWorktreePathStillCounts: WorktreePath is a real
// local checkout even when the session that owns it runs remotely, so it must
// keep counting as occupied.
func TestLocalSessionPaths_RemoteWorktreePathStillCounts(t *testing.T) {
	remote := sshInstance("a", "remote", "alice@host-a", "/srv/app-a")
	remote.WorktreePath = "/home/dev/repo-worktrees/feature-z"

	paths := localSessionPaths([]*session.Instance{remote})
	if !paths[remote.WorktreePath] {
		t.Error("a real local worktree stopped counting as occupied because its session runs remotely")
	}
}

// --- Site 4: findSessionByTmux path fallback ---------------------------------

// TestLocalSessionsAtPath_TmuxFallbackIgnoresRemote pins the fallback used for
// non-agentdeck tmux sessions: a pane's cwd is a local path, so a remote session
// must never be attributed to it.
func TestLocalSessionsAtPath_TmuxFallbackIgnoresRemote(t *testing.T) {
	instances := []*session.Instance{
		sshInstance("aaaaaaaaaaaaaaaa", "remote", "alice@host-a", "/srv/app-a"),
	}

	if got := localSessionsAtPath(instances, controllerCWD); len(got) != 0 {
		t.Fatalf("a pane at %s was attributed to remote session %s", controllerCWD, got[0].ID)
	}
	if got := localSessionForPaneCwd(instances, controllerCWD); got != nil {
		t.Fatalf("localSessionForPaneCwd attributed the pane to remote session %s", got.ID)
	}
}

// TestLocalSessionForPaneCwd_TwoLocalSessionsStillSelfDetect is review finding
// F7. Excluding remote sessions is the fix; refusing when two LOCAL sessions
// share a cwd is a separate decision that would take self-detection away from
// the very workflow #1850's auto-rename exists to support.
func TestLocalSessionForPaneCwd_TwoLocalSessionsStillSelfDetect(t *testing.T) {
	instances := []*session.Instance{
		{ID: "a", Title: "one", ProjectPath: "/home/dev/repo"},
		{ID: "b", Title: "two", ProjectPath: "/home/dev/repo"},
		sshInstance("c", "remote", "alice@host-a", "/srv/app-a"),
	}

	got := localSessionForPaneCwd(instances, "/home/dev/repo")
	if got == nil {
		t.Fatal("two LOCAL sessions in one directory now resolve to NOTHING — a pane there can no longer detect the session it sits in (F7)")
	}
	if got.ID != "a" {
		t.Fatalf("pane cwd resolved to %s, want the first registered local session 'a'", got.ID)
	}
}

// --- Site 5: `try` occupancy check -------------------------------------------

// TestLocalSessionsAtPath_TryIgnoresRemote pins site 5: `try` has no --ssh
// support, so a remote session whose placeholder equals the experiment path must
// not be adopted and started.
func TestLocalSessionsAtPath_TryIgnoresRemote(t *testing.T) {
	expPath := "/home/dev/experiments/spike"

	remote := sshInstance("aaaaaaaaaaaaaaaa", "remote", "alice@host-a", "/srv/app-a")
	remote.ProjectPath = expPath

	if got := localSessionsAtPath([]*session.Instance{remote}, expPath); len(got) != 0 {
		t.Fatalf("`try %s` would have started remote session %s", expPath, got[0].ID)
	}

	local := &session.Instance{ID: "bbbbbbbbbbbbbbbb", Title: "spike", ProjectPath: expPath}
	got := localSessionsAtPath([]*session.Instance{remote, local}, expPath)
	if len(got) != 1 || got[0].ID != local.ID {
		t.Fatalf("`try %s` did not pick the local session; got %v", expPath, got)
	}
}

// --- The identifier parser ---------------------------------------------------

func TestParseLocation(t *testing.T) {
	cases := []struct {
		in       string
		wantOK   bool
		wantHost string
		wantPath string
	}{
		{"alice@host-a:/srv/app", true, "alice@host-a", "/srv/app"},
		{"host-a:/srv/app/", true, "host-a", "/srv/app"},
		{"host-a:~/app", true, "host-a", "~/app"},
		{"host-a:~", true, "host-a", ""},    // the remote home, canonical form
		{"/srv/app", false, "", ""},         // bare path
		{"C:\\src\\project", false, "", ""}, // windows drive letter
		{"foo:bar", false, "", ""},          // relative remote path — not a location
		{":/srv/app", false, "", ""},        // no host
		{"/home/a:/srv/app", false, "", ""}, // host part contains a slash
		{"my session", false, "", ""},       // title-ish
	}
	for _, c := range cases {
		got, ok := session.ParseLocation(c.in)
		if ok != c.wantOK {
			t.Errorf("ParseLocation(%q) ok = %v, want %v", c.in, ok, c.wantOK)
			continue
		}
		if ok && got != session.RemoteLocation(c.wantHost, c.wantPath) {
			t.Errorf("ParseLocation(%q) = %+v, want host %q path %q", c.in, got, c.wantHost, c.wantPath)
		}
	}
}
