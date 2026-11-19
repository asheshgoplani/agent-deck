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

// Two sessions can share one worktree (`add -w <branch>` reuses an existing
// worktree for the branch). `worktree finish` on one of them removed the
// worktree and deleted the branch with no warning, leaving the other
// registered at a path that no longer exists. `remove` and
// `session remove --prune-worktree` removed it the same way. The TUI already
// keeps a shared worktree (#1449).

// sharedWorktreeSessions creates a git repo with one linked worktree and
// registers two sessions on it. It returns the two session ids, the worktree
// path and the repo root.
func sharedWorktreeSessions(t *testing.T, home string) (first, second, wt, repo string) {
	t.Helper()
	repo = filepath.Join(home, "repo")
	wt = filepath.Join(home, "wt-shared")
	for _, args := range [][]string{
		{"init", "-q", "-b", "main", repo},
		{"-C", repo, "-c", "user.email=t@t", "-c", "user.name=t", "commit", "-q", "--allow-empty", "-m", "init"},
		{"-C", repo, "worktree", "add", "-q", "-b", "feat-shared", wt},
	} {
		if out, err := exec.Command("git", args...).CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	first = addTestSession(t, home, filepath.Join(home, "p1"), "wt-one")
	second = addTestSession(t, home, filepath.Join(home, "p2"), "wt-four")

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
	for _, inst := range instances {
		if inst.ID == first || inst.ID == second {
			inst.Status = session.StatusStopped
			inst.WorktreePath = wt
			inst.WorktreeRepoRoot = repo
			inst.WorktreeBranch = "feat-shared"
		}
	}
	if err := storage.SaveWithGroups(instances, session.NewGroupTreeWithGroups(instances, groups)); err != nil {
		t.Fatalf("save: %v", err)
	}
	return first, second, wt, repo
}

func branchExists(t *testing.T, repo, branch string) bool {
	t.Helper()
	return exec.Command("git", "-C", repo, "rev-parse", "--verify", "--quiet", "refs/heads/"+branch).Run() == nil
}

func TestWorktreeFinish_RefusesSharedWorktree(t *testing.T) {
	if testing.Short() {
		t.Skip("subprocess CLI test skipped in short mode")
	}
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not on PATH")
	}
	home := t.TempDir()
	first, second, wt, repo := sharedWorktreeSessions(t, home)

	stdout, stderr, code := runAgentDeck(t, home, "worktree", "finish", first, "--no-merge", "--json")
	if code != 1 {
		t.Fatalf("worktree finish on a shared worktree: exit %d, want 1\n%s\n%s", code, stdout, stderr)
	}
	var res map[string]interface{}
	if err := json.Unmarshal([]byte(stdout), &res); err != nil {
		t.Fatalf("finish --json output: %v\n%s", err, stdout)
	}
	if msg, _ := res["error"].(string); !strings.Contains(msg, "wt-four") || !strings.Contains(msg, "--force") {
		t.Errorf("refusal must name the other session and --force, got %q", msg)
	}
	if _, err := os.Stat(wt); err != nil {
		t.Errorf("refused finish removed the shared worktree: %v", err)
	}
	if !branchExists(t, repo, "feat-shared") {
		t.Error("refused finish deleted the shared branch")
	}
	list := readSessionsJSON(t, home)
	if !strings.Contains(list, first) || !strings.Contains(list, second) {
		t.Errorf("refused finish removed a session:\n%s", list)
	}

	// --force is the explicit override.
	if stdout, stderr, code := runAgentDeck(t, home, "worktree", "finish", first, "--no-merge", "--force", "--json"); code != 0 {
		t.Fatalf("worktree finish --force: exit %d\n%s\n%s", code, stdout, stderr)
	}
	if strings.Contains(readSessionsJSON(t, home), first) {
		t.Error("finish --force left the session listed")
	}
}

// remove and session remove --prune-worktree drop the session but keep a
// worktree another session still uses, as the TUI delete does.
func TestRemove_KeepsSharedWorktree(t *testing.T) {
	if testing.Short() {
		t.Skip("subprocess CLI test skipped in short mode")
	}
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not on PATH")
	}
	for _, tc := range []struct {
		name string
		args func(id string) []string
	}{
		{"remove", func(id string) []string { return []string{"remove", id, "--json"} }},
		{"session remove --prune-worktree", func(id string) []string {
			return []string{"session", "remove", id, "--prune-worktree", "--json"}
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			home := t.TempDir()
			first, second, wt, repo := sharedWorktreeSessions(t, home)
			stdout, stderr, code := runAgentDeck(t, home, tc.args(first)...)
			if code != 0 {
				t.Fatalf("%s: exit %d\n%s\n%s", tc.name, code, stdout, stderr)
			}
			list := readSessionsJSON(t, home)
			if strings.Contains(list, first) || !strings.Contains(list, second) {
				t.Fatalf("want only %s removed:\n%s", first, list)
			}
			if _, err := os.Stat(wt); err != nil {
				t.Errorf("%s removed a worktree that %s still uses: %v", tc.name, second, err)
			}
			if !branchExists(t, repo, "feat-shared") {
				t.Errorf("%s deleted the shared branch", tc.name)
			}

			// The last user takes the worktree with it, as before.
			if stdout, stderr, code := runAgentDeck(t, home, tc.args(second)...); code != 0 {
				t.Fatalf("%s (last user): exit %d\n%s\n%s", tc.name, code, stdout, stderr)
			}
			if _, err := os.Stat(wt); !os.IsNotExist(err) {
				t.Errorf("%s of the last user kept the worktree (stat err %v)", tc.name, err)
			}
		})
	}
}

// A session added by plain path at (or inside) the worktree records no
// worktree fields; it still uses the directory finish would remove.
func TestWorktreeFinish_CountsPlainPathSessionAsSharer(t *testing.T) {
	if testing.Short() {
		t.Skip("subprocess CLI test skipped in short mode")
	}
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not on PATH")
	}
	home := t.TempDir()
	first, second, wt, _ := sharedWorktreeSessions(t, home)
	// Only first keeps the worktree fields; a plain session lives in a subdir.
	if stdout, stderr, code := runAgentDeck(t, home, "session", "remove", second, "--json"); code != 0 {
		t.Fatalf("drop second: exit %d\n%s\n%s", code, stdout, stderr)
	}
	if _, err := os.Stat(wt); err != nil {
		t.Fatalf("worktree gone before the test: %v", err)
	}
	addTestSession(t, home, filepath.Join(wt, "sub"), "plain-in-wt")

	stdout, stderr, code := runAgentDeck(t, home, "worktree", "finish", first, "--no-merge", "--json")
	if code != 1 || !strings.Contains(stdout, "plain-in-wt") {
		t.Fatalf("finish with a plain-path session inside the worktree: exit %d, want 1 naming plain-in-wt\n%s\n%s", code, stdout, stderr)
	}
	if _, err := os.Stat(wt); err != nil {
		t.Errorf("refused finish removed the worktree: %v", err)
	}
}

// --force overrides the guard and says whom it left without a worktree;
// remove reports a kept worktree in --json.
func TestWorktreeShared_JSONReportsOverrideAndKeep(t *testing.T) {
	if testing.Short() {
		t.Skip("subprocess CLI test skipped in short mode")
	}
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not on PATH")
	}
	home := t.TempDir()
	first, second, _, _ := sharedWorktreeSessions(t, home)
	stdout, stderr, code := runAgentDeck(t, home, "remove", first, "--json")
	if code != 0 {
		t.Fatalf("remove: exit %d\n%s\n%s", code, stdout, stderr)
	}
	var rm map[string]interface{}
	if err := json.Unmarshal([]byte(stdout), &rm); err != nil || rm["worktree_kept"] != true {
		t.Errorf("remove --json: want worktree_kept=true, got %v (%v)\n%s", rm["worktree_kept"], err, stdout)
	}

	home2 := t.TempDir()
	first, _, _, _ = sharedWorktreeSessions(t, home2)
	_ = second
	stdout, stderr, code = runAgentDeck(t, home2, "worktree", "finish", first, "--no-merge", "--force", "--json")
	if code != 0 {
		t.Fatalf("finish --force: exit %d\n%s\n%s", code, stdout, stderr)
	}
	// Progress lines precede the JSON object on stdout; read the object.
	obj := stdout[strings.Index(stdout, "{"):]
	var fin struct {
		SharedWith []string `json:"shared_with"`
	}
	if err := json.Unmarshal([]byte(obj), &fin); err != nil || len(fin.SharedWith) != 1 || fin.SharedWith[0] != "wt-four" {
		t.Errorf("finish --force --json: want shared_with [wt-four], got %v (%v)\n%s", fin.SharedWith, err, stdout)
	}
}

// The bulk sweep keeps a worktree that a surviving session uses and removes
// one whose every user is in the sweep.
func TestSessionRemoveAllErrored_PruneSharedWorktree(t *testing.T) {
	if testing.Short() {
		t.Skip("subprocess CLI test skipped in short mode")
	}
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not on PATH")
	}
	for _, tc := range []struct {
		name         string
		bothErrored  bool
		wantWorktree bool
	}{
		{"one sharer survives", false, true},
		{"every sharer swept", true, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			home := t.TempDir()
			first, second, wt, _ := sharedWorktreeSessions(t, home)
			forceSetStatus(t, home, first, session.StatusError)
			if tc.bothErrored {
				forceSetStatus(t, home, second, session.StatusError)
			}
			if stdout, stderr, code := runAgentDeck(t, home, "session", "remove", "--all-errored", "--prune-worktree", "--json"); code != 0 {
				t.Fatalf("bulk remove: exit %d\n%s\n%s", code, stdout, stderr)
			}
			_, err := os.Stat(wt)
			if got := err == nil; got != tc.wantWorktree {
				t.Errorf("worktree present = %v, want %v (stat err %v)", got, tc.wantWorktree, err)
			}
		})
	}
}

// A session in another local profile that uses the worktree blocks finish
// too: removing the worktree would strand it just the same.
func TestWorktreeFinish_RefusesWorktreeUsedByOtherProfile(t *testing.T) {
	if testing.Short() {
		t.Skip("subprocess CLI test skipped in short mode")
	}
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not on PATH")
	}
	home := t.TempDir()
	first, second, wt, repo := sharedWorktreeSessions(t, home)
	// Leave first as the only user in its own profile; the other user lives
	// in a second profile.
	if stdout, stderr, code := runAgentDeck(t, home, "session", "remove", second, "--json"); code != 0 {
		t.Fatalf("drop second: exit %d\n%s\n%s", code, stdout, stderr)
	}
	if stdout, stderr, code := runAgentDeck(t, home, "-p", "other_profile_test",
		"add", "-t", "wt-other-profile", "-c", "claude", "--no-parent", "--json", wt); code != 0 {
		t.Fatalf("add in other profile: exit %d\n%s\n%s", code, stdout, stderr)
	}

	stdout, stderr, code := runAgentDeck(t, home, "worktree", "finish", first, "--no-merge", "--json")
	if code != 1 || !strings.Contains(stdout, "wt-other-profile") {
		t.Fatalf("finish with the worktree used in another profile: exit %d, want 1 naming wt-other-profile\n%s\n%s", code, stdout, stderr)
	}
	if _, err := os.Stat(wt); err != nil {
		t.Errorf("refused finish removed the worktree: %v", err)
	}
	if !branchExists(t, repo, "feat-shared") {
		t.Error("refused finish deleted the shared branch")
	}
}
