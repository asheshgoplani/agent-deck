package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/asheshgoplani/agent-deck/internal/git"
	"github.com/asheshgoplani/agent-deck/internal/session"
)

// session fork -g must resolve a group selector exactly like add -g and
// launch -g (resolveGroupPathForAdd). Before the fix fork used the raw
// selector, so "fork -g backend" with only platform/backend stored created a
// new top-level "backend" group, and an ambiguous name was not refused.

// forkGroupResolveProfile is the profile agentDeckTestEnv gives the CLI
// binary, so the ambiguous case can run the real fork in a subprocess.
const forkGroupResolveProfile = "ch_support_test"

// setupForkGroupResolveTest seeds a forkable Claude session "worker" plus the
// groups platform/backend and two groups named workers (team/workers,
// yasir/workers) in an isolated HOME.
func setupForkGroupResolveTest(t *testing.T) string {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))
	t.Setenv("XDG_DATA_HOME", filepath.Join(home, ".local", "share"))
	session.ClearUserConfigCache()
	t.Cleanup(session.ClearUserConfigCache)

	source := session.NewInstanceWithGroupAndTool("worker", home, "src", "claude")
	source.ClaudeSessionID = "00000000-0000-4000-8000-000000000092"
	source.ClaudeDetectedAt = time.Now()
	instances := []*session.Instance{source}

	tree := session.NewGroupTreeWithGroups(instances, nil)
	for _, p := range []string{"platform/backend", "team/workers", "yasir/workers"} {
		if tree.CreateGroupPath(p) == nil {
			t.Fatalf("CreateGroupPath(%q) failed", p)
		}
	}
	storage, err := session.NewStorageWithProfile(forkGroupResolveProfile)
	if err != nil {
		t.Fatalf("NewStorageWithProfile: %v", err)
	}
	t.Cleanup(func() { _ = storage.Close() })
	if err := storage.SaveWithGroups(instances, tree); err != nil {
		t.Fatalf("SaveWithGroups: %v", err)
	}
	return home
}

// forkGroupOf runs session fork in process up to its pre-start hook and
// returns the group the fork would be created in.
func forkGroupOf(t *testing.T, args ...string) string {
	t.Helper()
	var forked *session.Instance
	oldHook := sessionForkBeforeStartHook
	sessionForkBeforeStartHook = func(_ *session.Instance, f *session.Instance, _ git.WorktreeStateOptions) { forked = f }
	t.Cleanup(func() { sessionForkBeforeStartHook = oldHook })

	handleSessionFork(forkGroupResolveProfile, args)
	if forked == nil {
		t.Fatal("CLI fork did not reach the pre-start hook")
	}
	return forked.GroupPath
}

func TestSessionFork_GroupUniqueNestedNameResolvesLikeAdd(t *testing.T) {
	setupForkGroupResolveTest(t)

	if got := forkGroupOf(t, "worker", "-t", "f1", "-g", "backend"); got != "platform/backend" {
		t.Fatalf("fork -g backend landed in %q, want platform/backend", got)
	}
}

// A name matching no group keeps add's behaviour: the group is created under
// the name given.
func TestSessionFork_GroupNewNameKeptLikeAdd(t *testing.T) {
	setupForkGroupResolveTest(t)

	if got := forkGroupOf(t, "worker", "-t", "f3", "--group", "brandnew"); got != "brandnew" {
		t.Fatalf("fork --group brandnew landed in %q, want brandnew", got)
	}
}

// The ambiguous case exits, so it runs the real CLI binary. A no-op tmux
// first on PATH keeps the unfixed fork, which goes on to start, off any real
// tmux server.
func TestSessionFork_GroupAmbiguousNameFailsLikeAdd(t *testing.T) {
	if testing.Short() {
		t.Skip("subprocess CLI test skipped in short mode")
	}
	home := setupForkGroupResolveTest(t)
	binDir := t.TempDir()
	fakeTmux := "#!/bin/sh\ncase \" $* \" in\n  *' has-session '*) exit 1 ;;\nesac\nexit 0\n"
	if err := os.WriteFile(filepath.Join(binDir, "tmux"), []byte(fakeTmux), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", binDir+string(os.PathListSeparator)+os.Getenv("PATH"))

	stdout, stderr, code := runAgentDeck(t, home, "session", "fork", "worker", "-t", "f2", "-g", "workers")
	want := `group "workers" is ambiguous: team/workers, yasir/workers; use the full path`
	if code == 0 || !strings.Contains(stdout+stderr, want) {
		t.Fatalf("fork -g workers: exit=%d, want non-zero and %q\nstdout: %s\nstderr: %s", code, want, stdout, stderr)
	}
	for _, p := range launchGroupPaths(t, home) {
		if p == "workers" {
			t.Fatalf("ambiguous fork created a top-level workers group")
		}
	}
	stdout, stderr, code = runAgentDeck(t, home, "list", "--json")
	if code != 0 || strings.Contains(stdout, `"f2"`) {
		t.Fatalf("ambiguous fork registered a session (exit=%d)\n%s\n%s", code, stdout, stderr)
	}
}
