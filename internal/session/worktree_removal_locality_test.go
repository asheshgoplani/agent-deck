package session

import (
	"os"
	"path/filepath"
	"testing"
)

// A remote (--ssh) session's ProjectPath is only a local placeholder (often the
// directory it was created from), so it never uses a local worktree. Counting
// it made worktree finish refuse, and every other removal keep the worktree,
// because of an SSH session in any local profile created from inside it.
// A local multi-repo session uses its per-repo worktrees and its additional
// paths, so those do count.
func TestSessionsSharingWorktree_OnlyLocalUsersCount(t *testing.T) {
	wt := filepath.Join(t.TempDir(), "wt")
	if err := os.MkdirAll(filepath.Join(wt, "sub"), 0o755); err != nil {
		t.Fatal(err)
	}
	elsewhere := t.TempDir()
	target := &Instance{ID: "t", WorktreePath: wt, WorktreeRepoRoot: "/repo"}
	for _, tc := range []struct {
		name  string
		other *Instance
		want  int
	}{
		{"remote session with its placeholder at the worktree", &Instance{ID: "r", Title: "remote", ProjectPath: wt, SSHHost: "box", SSHRemotePath: "/srv/x"}, 0},
		{"remote session with its placeholder inside the worktree", &Instance{ID: "r2", ProjectPath: filepath.Join(wt, "sub"), SSHHost: "box"}, 0},
		{"remote session carrying the worktree path", &Instance{ID: "r3", ProjectPath: elsewhere, WorktreePath: wt, SSHHost: "box", SSHRemotePath: "/srv/x"}, 0},
		{"local session at the worktree", &Instance{ID: "l", ProjectPath: wt}, 1},
		{"local session inside the worktree", &Instance{ID: "l2", ProjectPath: filepath.Join(wt, "sub")}, 1},
		{"local multi-repo session with the worktree as a per-repo worktree", &Instance{ID: "m", ProjectPath: elsewhere, MultiRepoEnabled: true,
			MultiRepoWorktrees: []MultiRepoWorktree{{WorktreePath: wt, RepoRoot: "/repo"}}}, 1},
		{"local session with the worktree as an additional path", &Instance{ID: "a", ProjectPath: elsewhere, MultiRepoEnabled: true,
			AdditionalPaths: []string{filepath.Join(wt, "sub")}}, 1},
		{"remote multi-repo session with the worktree as a per-repo worktree", &Instance{ID: "rm", ProjectPath: elsewhere, SSHHost: "box",
			MultiRepoWorktrees: []MultiRepoWorktree{{WorktreePath: wt}}}, 0},
		{"local session elsewhere", &Instance{ID: "e", ProjectPath: elsewhere}, 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := SessionsSharingWorktree(target, []*Instance{tc.other}); len(got) != tc.want {
				t.Errorf("SessionsSharingWorktree with %s (at %s) = %d users, want %d", tc.name, LocationOf(tc.other), len(got), tc.want)
			}
		})
	}
}
