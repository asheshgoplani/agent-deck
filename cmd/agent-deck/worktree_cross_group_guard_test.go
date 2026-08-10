package main

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// TestLaunchRejectsExplicitGroupThatConflictsWithLinkedWorktreeParent reproduces
// the real uniqcast/doozyx/uniqcast misplacement through the CLI. The rejection
// must happen before launch persistence or tmux startup.
func TestLaunchRejectsExplicitGroupThatConflictsWithLinkedWorktreeParent(t *testing.T) {
	home := t.TempDir()
	repo := filepath.Join(home, "Uniqcast", "tvmid-core")
	worktree := filepath.Join(repo, ".worktrees", "bugfix-7.17.3")
	if err := os.MkdirAll(repo, 0o755); err != nil {
		t.Fatal(err)
	}
	runGitForCrossGroupTest(t, repo, "init", "-q")
	runGitForCrossGroupTest(t, repo, "config", "user.email", "test@example.com")
	runGitForCrossGroupTest(t, repo, "config", "user.name", "Test User")
	if err := os.WriteFile(filepath.Join(repo, "README"), []byte("seed\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	runGitForCrossGroupTest(t, repo, "add", "README")
	runGitForCrossGroupTest(t, repo, "commit", "-qm", "seed")
	runGitForCrossGroupTest(t, repo, "worktree", "add", "-qb", "bugfix-7.17.3", worktree)

	if stdout, stderr, code := runCrossGroupAgentDeck(t, home,
		"add", "-c", "bash", "-t", "parent", "-g", "uniqcast", repo,
	); code != 0 {
		t.Fatalf("seed parent: exit=%d stdout=%s stderr=%s", code, stdout, stderr)
	}

	socket := isolatedTmuxSocket1031(t)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, crossGroupCLIBinary(t),
		"launch", "--title", "e2e-CSMS-7168", "--parent", "parent",
		"--group", "doozyx/uniqcast", "--tmux-socket", socket, "-c", "true", worktree,
	)
	cmd.WaitDelay = time.Second
	cmd.Env = cliEnvForIssue1031(home)
	var stdout, stderr strings.Builder
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	err := cmd.Run()
	if err == nil {
		t.Fatalf("conflicting worktree group was accepted; stdout=%s stderr=%s", stdout.String(), stderr.String())
	}
	if ctx.Err() != nil {
		t.Fatalf("launch hung instead of rejecting the conflicting group; stdout=%s stderr=%s", stdout.String(), stderr.String())
	}
	combined := stdout.String() + stderr.String()
	for _, want := range []string{"doozyx/uniqcast", "uniqcast", "--allow-cross-group"} {
		if !strings.Contains(combined, want) {
			t.Errorf("error does not mention %q; output=%s", want, combined)
		}
	}

	listOut, listErr, listCode := runCrossGroupAgentDeck(t, home, "list", "--json")
	if listCode != 0 {
		t.Fatalf("list after rejection: exit=%d stdout=%s stderr=%s", listCode, listOut, listErr)
	}
	if strings.Contains(listOut, "e2e-CSMS-7168") {
		t.Fatalf("rejected launch persisted a child session: %s", listOut)
	}
}

func TestValidateWorktreeCrossGroup(t *testing.T) {
	tests := []struct {
		name        string
		explicit    bool
		allow       bool
		parent      bool
		worktree    bool
		requested   string
		parentGroup string
		wantError   bool
	}{
		{name: "reject conflicting explicit group", explicit: true, parent: true, worktree: true, requested: "doozyx/uniqcast", parentGroup: "uniqcast", wantError: true},
		{name: "matching explicit group", explicit: true, parent: true, worktree: true, requested: "uniqcast", parentGroup: "uniqcast"},
		{name: "deliberate override", explicit: true, allow: true, parent: true, worktree: true, requested: "other", parentGroup: "uniqcast"},
		{name: "non-worktree child", explicit: true, parent: true, requested: "other", parentGroup: "uniqcast"},
		{name: "unparented worktree", explicit: true, worktree: true, requested: "other", parentGroup: "uniqcast"},
		{name: "implicit group", parent: true, worktree: true, requested: "uniqcast", parentGroup: "uniqcast"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := validateWorktreeCrossGroup(tt.explicit, tt.allow, tt.parent, tt.worktree, tt.requested, tt.parentGroup)
			if (err != nil) != tt.wantError {
				t.Fatalf("validateWorktreeCrossGroup() error = %v, wantError=%v", err, tt.wantError)
			}
		})
	}
}

func runGitForCrossGroupTest(t *testing.T, dir string, args ...string) {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
}

func crossGroupCLIBinary(t *testing.T) string {
	t.Helper()
	if bin := os.Getenv("AGENT_DECK_CROSS_GROUP_TEST_BIN"); bin != "" {
		return bin
	}
	return channelsCLIBinary(t)
}

func runCrossGroupAgentDeck(t *testing.T, home string, args ...string) (string, string, int) {
	t.Helper()
	cmd := exec.Command(crossGroupCLIBinary(t), args...)
	cmd.Env = cliEnvForIssue1031(home)
	var stdout, stderr strings.Builder
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	err := cmd.Run()
	if err == nil {
		return stdout.String(), stderr.String(), 0
	}
	if exitErr, ok := err.(*exec.ExitError); ok {
		return stdout.String(), stderr.String(), exitErr.ExitCode()
	}
	t.Fatalf("agent-deck %v: %v", args, err)
	return "", "", -1
}
