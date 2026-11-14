package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"github.com/asheshgoplani/agent-deck/internal/session"
)

// Issue #2806 (round-2 review): a removal kills the session first and deletes
// the row last; with a worktree, `git worktree remove` runs in between and can
// take longer than the notify daemon's death grace. The row must already read
// stopped while the worktree removal runs, so the daemon never takes the gone
// pane for an idle child that died.
//
// A `git` shim on PATH holds `worktree remove` until the test has read the row.

func issue2806GitShim(t *testing.T) (binDir, mark string) {
	t.Helper()
	realGit, err := exec.LookPath("git")
	if err != nil {
		t.Skip("git not on PATH")
	}
	binDir = t.TempDir()
	mark = t.TempDir()
	script := `#!/bin/sh
case " $* " in
*" worktree remove "*)
	: > "` + mark + `/started"
	i=0
	while [ ! -e "` + mark + `/go" ] && [ $i -lt 300 ]; do sleep 0.1; i=$((i+1)); done
	;;
esac
exec "` + realGit + `" "$@"
`
	if err := os.WriteFile(filepath.Join(binDir, "git"), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	return binDir, mark
}

// issue2806WorktreeSession adds a session and points it at a real linked git
// worktree, with the given saved status.
func issue2806WorktreeSession(t *testing.T, home, title string, status session.Status) string {
	t.Helper()
	repo := filepath.Join(home, "repo-"+title)
	wt := filepath.Join(home, "wt-"+title)
	for _, args := range [][]string{
		{"init", "-q", repo},
		{"-C", repo, "-c", "user.email=t@t", "-c", "user.name=t", "commit", "-q", "--allow-empty", "-m", "init"},
		{"-C", repo, "worktree", "add", "-q", "-b", "br-" + title, wt},
	} {
		if out, err := exec.Command("git", args...).CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	id := addTestSession(t, home, filepath.Join(home, "proj-"+title), title)

	t.Setenv("HOME", home)
	t.Setenv("AGENTDECK_PROFILE", "ch_support_test")
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))
	t.Setenv("XDG_DATA_HOME", filepath.Join(home, ".local", "share"))
	storage, err := session.NewStorageWithProfile("")
	if err != nil {
		t.Fatalf("open storage: %v", err)
	}
	defer storage.Close()
	instances, groups, err := storage.LoadWithGroups()
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	found := false
	for _, inst := range instances {
		if inst.ID == id {
			inst.Status = status
			inst.WorktreePath = wt
			inst.WorktreeRepoRoot = repo
			inst.WorktreeBranch = "br-" + title
			found = true
		}
	}
	if !found {
		t.Fatalf("instance %s not found", id)
	}
	if err := storage.SaveWithGroups(instances, session.NewGroupTreeWithGroups(instances, groups)); err != nil {
		t.Fatalf("save: %v", err)
	}
	return id
}

// issue2806RowStatus reads the saved status of one row ("" = row gone).
func issue2806RowStatus(t *testing.T, id string) string {
	t.Helper()
	storage, err := session.NewStorageWithProfile("")
	if err != nil {
		t.Fatalf("open storage: %v", err)
	}
	defer storage.Close()
	row, err := storage.GetDB().LoadInstanceByID(id)
	if err != nil {
		t.Fatalf("load %s: %v", id, err)
	}
	if row == nil {
		return ""
	}
	return row.Status
}

func TestIssue2806_RemoveSavesStoppedBeforeWorktreeRemoval(t *testing.T) {
	if testing.Short() {
		t.Skip("subprocess CLI test skipped in short mode")
	}
	cases := []struct {
		name   string
		status session.Status
		args   func(id string) []string
	}{
		{"session_remove_force_prune", session.StatusWaiting, func(id string) []string {
			return []string{"session", "remove", id, "--force", "--prune-worktree", "--json"}
		}},
		{"session_remove_all_errored_prune", session.StatusError, func(string) []string {
			return []string{"session", "remove", "--all-errored", "--prune-worktree", "--json"}
		}},
		{"remove", session.StatusWaiting, func(id string) []string {
			return []string{"remove", id, "--json"}
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			home := t.TempDir()
			id := issue2806WorktreeSession(t, home, "rm2806", tc.status)
			binDir, mark := issue2806GitShim(t)
			bin := channelsCLIBinary(t)
			env := []string{"PATH=" + binDir + string(os.PathListSeparator) + os.Getenv("PATH")}

			type result struct {
				stdout, stderr string
				code           int
				err            error
			}
			done := make(chan result, 1)
			go func() {
				o, e, c, err := execAgentDeckEnv(bin, home, "", env, tc.args(id)...)
				done <- result{o, e, c, err}
			}()

			deadline := time.Now().Add(60 * time.Second)
			for {
				if _, err := os.Stat(filepath.Join(mark, "started")); err == nil {
					break
				}
				select {
				case r := <-done:
					t.Fatalf("remove finished without reaching `git worktree remove` (exit %d, %v)\nstdout: %s\nstderr: %s", r.code, r.err, r.stdout, r.stderr)
				default:
				}
				if time.Now().After(deadline) {
					_ = os.WriteFile(filepath.Join(mark, "go"), nil, 0o644)
					t.Fatal("`git worktree remove` never started")
				}
				time.Sleep(50 * time.Millisecond)
			}
			during := issue2806RowStatus(t, id)
			if err := os.WriteFile(filepath.Join(mark, "go"), nil, 0o644); err != nil {
				t.Fatal(err)
			}
			var r result
			select {
			case r = <-done:
			case <-time.After(60 * time.Second):
				t.Fatal("remove did not finish after the worktree removal was released")
			}
			if r.err != nil || r.code != 0 {
				t.Fatalf("remove: exit %d, %v\nstdout: %s\nstderr: %s", r.code, r.err, r.stdout, r.stderr)
			}
			if during != string(session.StatusStopped) {
				t.Errorf("row status while the worktree was being removed = %q, want %q (the notify daemon would report an idle death)", during, session.StatusStopped)
			}
			if after := issue2806RowStatus(t, id); after != "" {
				t.Errorf("row still present after remove: status %q", after)
			}
		})
	}
}
