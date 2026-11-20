package ui

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/asheshgoplani/agent-deck/internal/session"
)

// A refused create (default account on a host with no login) must leave no
// worktree, branch or session behind.
func TestCreateSession_AccountRefusalLeavesNoWorktree(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	for _, k := range []string{"CLAUDE_CONFIG_DIR", "AGENTDECK_PROFILE", "AGENTDECK_ACCOUNT"} {
		t.Setenv(k, "")
	}
	session.ClearUserConfigCache()
	t.Cleanup(session.ClearUserConfigCache)
	prev := session.ClaudeLoginProbe
	session.ClaudeLoginProbe = func(string) bool { return false }
	t.Cleanup(func() { session.ClaudeLoginProbe = prev })

	repo := filepath.Join(home, "repo")
	if err := os.MkdirAll(repo, 0o755); err != nil {
		t.Fatal(err)
	}
	git := func(args ...string) string {
		t.Helper()
		cmd := exec.Command("git", append([]string{"-c", "user.name=t", "-c", "user.email=t@example.com", "-c", "init.defaultBranch=main"}, args...)...)
		cmd.Dir = repo
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
		return string(out)
	}
	git("init", "-q")
	git("commit", "-q", "--allow-empty", "-m", "init")
	wt := filepath.Join(home, "wt", "feature")

	h := &Home{}
	msg := h.createSessionInGroupWithWorktreeAndOptions(
		"refused", repo, "claude", "claude", "test",
		wt, repo, "feature",
		false, false, nil, nil, "", "", "", false, nil, "", "", "", false,
	)().(sessionCreatedMsg)
	if msg.err == nil {
		t.Fatal("want a refusal on a host with no login")
	}
	if !strings.Contains(msg.err.Error(), "agent-deck accounts") {
		t.Fatalf("refusal must name the fix, got: %v", msg.err)
	}
	if _, err := os.Stat(wt); !os.IsNotExist(err) {
		t.Fatalf("worktree left behind at %s (stat err: %v)", wt, err)
	}
	if out := git("branch", "--list", "feature"); strings.TrimSpace(out) != "" {
		t.Fatalf("branch left behind: %q", out)
	}
}
