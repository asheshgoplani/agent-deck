package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/asheshgoplani/agent-deck/internal/session"
	"github.com/asheshgoplani/agent-deck/internal/statedb"
)

// Every CLI path that removes a session's worktree must count users in other
// local profiles, not only the current one. `worktree finish` got that check
// first; `session remove --prune-worktree`, `remove`,
// `session remove --all-errored --prune-worktree` and
// `session cleanup --prune-worktree` still removed a worktree that a session
// in another profile was using.
//
// `session cleanup` needs a live tmux server to judge a session dead, so it
// has its own sandboxed test below. `worktree cleanup` is covered by TestCleanupReviewCrossProfileBoundary and
// `worktree finish` by TestWorktreeFinish_RefusesWorktreeUsedByOtherProfile.

const otherProfileForWorktreeTests = "other_profile_test"

// worktreeWithOtherProfileUser builds the two-profile layout: first is the
// only session on the worktree in the current profile, and a second session
// lives in another local profile. When otherUses is false that second
// session sits at an unrelated path (the positive control).
func worktreeWithOtherProfileUser(t *testing.T, home string, otherUses bool) (first, wt, repo string) {
	t.Helper()
	first, second, wt, repo := sharedWorktreeSessions(t, home)
	if stdout, stderr, code := runAgentDeck(t, home, "session", "remove", second, "--json"); code != 0 {
		t.Fatalf("drop second: exit %d\n%s\n%s", code, stdout, stderr)
	}
	otherPath := wt
	if !otherUses {
		otherPath = filepath.Join(home, "elsewhere")
		if err := os.MkdirAll(otherPath, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	if stdout, stderr, code := runAgentDeck(t, home, "-p", otherProfileForWorktreeTests,
		"add", "-t", "wt-other-profile", "-c", "claude", "--no-parent", "--json", otherPath); code != 0 {
		t.Fatalf("add in other profile: exit %d\n%s\n%s", code, stdout, stderr)
	}
	if _, err := os.Stat(wt); err != nil {
		t.Fatalf("worktree gone before the test: %v", err)
	}
	return first, wt, repo
}

// worktreeRemovalCLIPaths are the session-removing commands that delete the
// session's worktree. prepare drives the session into the state the command
// selects.
var worktreeRemovalCLIPaths = []struct {
	name    string
	prepare func(t *testing.T, home, id string)
	args    func(id string) []string
}{
	{
		name: "session remove --prune-worktree",
		args: func(id string) []string { return []string{"session", "remove", id, "--prune-worktree", "--json"} },
	},
	{
		name: "remove",
		args: func(id string) []string { return []string{"remove", id, "--json"} },
	},
	{
		name:    "session remove --all-errored --prune-worktree",
		prepare: func(t *testing.T, home, id string) { forceSetStatus(t, home, id, session.StatusError) },
		args: func(string) []string {
			return []string{"session", "remove", "--all-errored", "--prune-worktree", "--json"}
		},
	},
}

func skipUnlessWorktreeCLITest(t *testing.T) {
	t.Helper()
	if testing.Short() {
		t.Skip("subprocess CLI test skipped in short mode")
	}
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not on PATH")
	}
}

func TestWorktreeRemovalPaths_KeepWorktreeUsedByOtherProfile(t *testing.T) {
	skipUnlessWorktreeCLITest(t)
	for _, tc := range worktreeRemovalCLIPaths {
		for _, otherUses := range []bool{true, false} {
			name := tc.name + "/other profile uses it"
			if !otherUses {
				name = tc.name + "/other profile elsewhere"
			}
			t.Run(name, func(t *testing.T) {
				home := t.TempDir()
				first, wt, repo := worktreeWithOtherProfileUser(t, home, otherUses)
				if tc.prepare != nil {
					tc.prepare(t, home, first)
				}
				stdout, stderr, code := runAgentDeck(t, home, tc.args(first)...)
				if code != 0 {
					t.Fatalf("%s: exit %d\n%s\n%s", tc.name, code, stdout, stderr)
				}
				if strings.Contains(readSessionsJSON(t, home), first) {
					t.Fatalf("%s left the session listed", tc.name)
				}
				_, err := os.Stat(wt)
				if otherUses {
					if err != nil {
						t.Errorf("%s removed a worktree that a session in another profile still uses: %v", tc.name, err)
					}
					if !branchExists(t, repo, "feat-shared") {
						t.Errorf("%s deleted the branch of a worktree another profile uses", tc.name)
					}
					if tc.name != "session remove --all-errored --prune-worktree" && !strings.Contains(stdout, `"worktree_kept": true`) {
						t.Errorf("%s --json did not report worktree_kept: true\n%s", tc.name, stdout)
					}
				} else if !os.IsNotExist(err) {
					t.Errorf("%s kept a worktree no session uses (stat err %v)\n%s\n%s", tc.name, err, stdout, stderr)
				}
			})
		}
	}
}

// When another profile cannot be read, nobody can tell whether it uses the
// worktree, so the removal keeps it; --force (where the command has one)
// removes it anyway, as `worktree finish --force` does.
func TestWorktreeRemovalPaths_UnreadableOtherProfileKeepsWorktree(t *testing.T) {
	skipUnlessWorktreeCLITest(t)
	for _, tc := range []struct {
		name         string
		args         func(id string) []string
		wantWorktree bool
	}{
		{"session remove --prune-worktree", func(id string) []string {
			return []string{"session", "remove", id, "--prune-worktree", "--json"}
		}, true},
		{"session remove --prune-worktree --force", func(id string) []string {
			return []string{"session", "remove", id, "--prune-worktree", "--force", "--json"}
		}, false},
		{"remove", func(id string) []string { return []string{"remove", id, "--json"} }, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			home := t.TempDir()
			first, second, wt, _ := sharedWorktreeSessions(t, home)
			// Only first uses the worktree in this profile.
			if stdout, stderr, code := runAgentDeck(t, home, "session", "remove", second, "--json"); code != 0 {
				t.Fatalf("drop second: exit %d\n%s\n%s", code, stdout, stderr)
			}
			broken := filepath.Join(home, ".local", "share", "agent-deck", "profiles", "broken_profile_test")
			if err := os.MkdirAll(broken, 0o700); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(broken, "state.db"), []byte("not a database"), 0o600); err != nil {
				t.Fatal(err)
			}
			stdout, stderr, code := runAgentDeck(t, home, tc.args(first)...)
			if code != 0 {
				t.Fatalf("%s: exit %d\n%s\n%s", tc.name, code, stdout, stderr)
			}
			_, err := os.Stat(wt)
			if got := err == nil; got != tc.wantWorktree {
				t.Errorf("%s with an unreadable profile: worktree present = %v, want %v\n%s\n%s", tc.name, got, tc.wantWorktree, stdout, stderr)
			}
		})
	}
}

// session cleanup --prune-worktree keeps a dead session's worktree while a
// session in another local profile uses it, and removes it otherwise.
func TestSessionCleanup_KeepsWorktreeUsedByOtherProfile(t *testing.T) {
	for _, otherUses := range []bool{true, false} {
		name := "other profile uses it"
		if !otherUses {
			name = "other profile elsewhere"
		}
		t.Run(name, func(t *testing.T) {
			sb := removeSandbox(t)
			startTmuxServer(sb)
			repo, wt := newSandboxWorktree(t, sb, "cleanup-xp")
			seedDeadSession(t, sb, "cleanup-xp", wt, repo)
			otherPath := wt
			if !otherUses {
				otherPath = sb.Home
			}
			dir := filepath.Join(sb.Home, ".local", "share", "agent-deck", "profiles", otherProfileForWorktreeTests)
			if err := os.MkdirAll(dir, 0o700); err != nil {
				t.Fatal(err)
			}
			db, err := statedb.Open(filepath.Join(dir, "state.db"))
			if err != nil {
				t.Fatal(err)
			}
			if err := db.Migrate(); err != nil {
				t.Fatal(err)
			}
			if err := db.SaveInstance(&statedb.InstanceRow{ID: "xp-owner", Title: "wt-other-profile", ProjectPath: otherPath,
				GroupPath: "other", Tool: "shell", Command: "bash", Status: "stopped", CreatedAt: time.Now()}); err != nil {
				t.Fatal(err)
			}
			_ = db.Close()

			out, err := runRemoveCLI(t, sb, "session", "cleanup", "--yes", "--prune-worktree")
			if err != nil {
				t.Fatalf("session cleanup --yes --prune-worktree: %v\n%s", err, out)
			}
			if row := loadSandboxRow(t, sb, "cleanup-xp"); row != nil {
				t.Fatalf("dead row still present after cleanup: %+v\n%s", row, out)
			}
			_, statErr := os.Stat(wt)
			if otherUses && statErr != nil {
				t.Errorf("session cleanup --prune-worktree removed a worktree that a session in another profile still uses: %v\n%s", statErr, out)
			}
			if !otherUses && !os.IsNotExist(statErr) {
				t.Errorf("session cleanup --prune-worktree kept a worktree no session uses (stat err %v)\n%s", statErr, out)
			}
		})
	}
}

// --force removes a worktree even when another profile cannot be read. With
// --json the stderr warning is suppressed, so the output must name the
// profiles that were not checked; without --force nothing is skipped and the
// field is absent.
func TestWorktreeRemovalPaths_ForceReportsUncheckedProfiles(t *testing.T) {
	skipUnlessWorktreeCLITest(t)
	for _, tc := range []struct {
		name    string
		prepare func(t *testing.T, home, id string)
		args    func(id string) []string
		want    bool
	}{
		{name: "session remove --prune-worktree --force", want: true,
			args: func(id string) []string {
				return []string{"session", "remove", id, "--prune-worktree", "--force", "--json"}
			}},
		{name: "session remove --all-errored --prune-worktree --force", want: true,
			prepare: func(t *testing.T, home, id string) { forceSetStatus(t, home, id, session.StatusError) },
			args: func(string) []string {
				return []string{"session", "remove", "--all-errored", "--prune-worktree", "--force", "--json"}
			}},
		{name: "session remove --prune-worktree", want: false,
			args: func(id string) []string { return []string{"session", "remove", id, "--prune-worktree", "--json"} }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			home := t.TempDir()
			first, second, _, _ := sharedWorktreeSessions(t, home)
			if stdout, stderr, code := runAgentDeck(t, home, "session", "remove", second, "--json"); code != 0 {
				t.Fatalf("drop second: exit %d\n%s\n%s", code, stdout, stderr)
			}
			broken := filepath.Join(home, ".local", "share", "agent-deck", "profiles", "broken_profile_test")
			if err := os.MkdirAll(broken, 0o700); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(broken, "state.db"), []byte("not a database"), 0o600); err != nil {
				t.Fatal(err)
			}
			if tc.prepare != nil {
				tc.prepare(t, home, first)
			}
			stdout, stderr, code := runAgentDeck(t, home, tc.args(first)...)
			if code != 0 {
				t.Fatalf("%s: exit %d\n%s\n%s", tc.name, code, stdout, stderr)
			}
			has := strings.Contains(stdout, `"profiles_unchecked"`) && strings.Contains(stdout, "broken_profile_test")
			if has != tc.want {
				t.Errorf("%s --json: profiles_unchecked naming broken_profile_test present = %v, want %v\n%s", tc.name, has, tc.want, stdout)
			}
		})
	}
}

// The help of every --force that also skips the unreadable-profile check says so.
func TestWorktreeRemovalForceHelpMentionsProfileCheck(t *testing.T) {
	skipUnlessWorktreeCLITest(t)
	for _, args := range [][]string{
		{"session", "remove", "--help"},
		{"session", "cleanup", "--help"},
		{"worktree", "finish", "--help"},
	} {
		stdout, stderr, _ := runAgentDeck(t, t.TempDir(), args...)
		if !strings.Contains(stdout+stderr, "profiles that cannot be read") {
			t.Errorf("%v: --force help does not say it skips the check of profiles that cannot be read\n%s%s", args, stdout, stderr)
		}
	}
}
