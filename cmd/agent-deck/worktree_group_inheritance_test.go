package main

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestLaunchLinkedWorktreeInheritsAutomaticParentNestedGroup(t *testing.T) {
	if testing.Short() {
		t.Skip("subprocess CLI test skipped in short mode")
	}
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not on PATH; linked-worktree test requires git")
	}
	if _, err := exec.LookPath("tmux"); err != nil {
		t.Skip("tmux not on PATH; launch CLI needs a real tmux server")
	}

	home := t.TempDir()
	repo := filepath.Join(home, "repo")
	linked := filepath.Join(repo, ".worktrees", "feature-x")
	if err := os.MkdirAll(repo, 0o755); err != nil {
		t.Fatalf("mkdir repo: %v", err)
	}

	runWorktreeInheritanceCommand(t, home, repo, nil, "git", "init", "-q")
	runWorktreeInheritanceCommand(t, home, repo, nil, "git", "config", "user.email", "test@example.com")
	runWorktreeInheritanceCommand(t, home, repo, nil, "git", "config", "user.name", "Test User")
	if err := os.WriteFile(filepath.Join(repo, "README"), []byte("seed\n"), 0o644); err != nil {
		t.Fatalf("write seed: %v", err)
	}
	runWorktreeInheritanceCommand(t, home, repo, nil, "git", "add", "README")
	runWorktreeInheritanceCommand(t, home, repo, nil, "git", "commit", "-qm", "seed")
	runWorktreeInheritanceCommand(t, home, repo, nil, "git", "worktree", "add", ".worktrees/feature-x", "-b", "feature-x")

	bin := channelsCLIBinary(t)
	runWorktreeInheritanceCommand(t, home, repo, nil, bin, "list", "--json")
	runWorktreeInheritanceCommand(t, home, repo, nil, bin,
		"add", "-t", "parent", "-g", "acme/backend", "-c", "shell", repo)

	listOut := runWorktreeInheritanceCommand(t, home, repo, nil, bin, "list", "--json")
	var sessions []struct {
		ID    string `json:"id"`
		Title string `json:"title"`
	}
	if err := json.Unmarshal([]byte(strings.TrimSpace(listOut)), &sessions); err != nil {
		t.Fatalf("parse parent list --json: %v\noutput: %s", err, listOut)
	}
	var parentID string
	for _, session := range sessions {
		if session.Title == "parent" {
			parentID = session.ID
			break
		}
	}
	if parentID == "" {
		t.Fatalf("parent session missing an ID in list --json: %s", listOut)
	}

	socket := isolatedTmuxSocket1031(t)
	runWorktreeInheritanceCommand(t, home, linked,
		[]string{"AGENTDECK_INSTANCE_ID=" + parentID}, bin,
		"launch", "--tmux-socket", socket, "-t", "child", "--json", linked)

	childListOut := runWorktreeInheritanceCommand(t, home, linked, nil, bin, "list", "--json")
	var childSessions []struct {
		Title           string `json:"title"`
		GroupPath       string `json:"group"`
		ParentSessionID string `json:"parent_id"`
	}
	if err := json.Unmarshal([]byte(strings.TrimSpace(childListOut)), &childSessions); err != nil {
		t.Fatalf("parse child list --json: %v\noutput: %s", err, childListOut)
	}
	var child *struct {
		Title           string `json:"title"`
		GroupPath       string `json:"group"`
		ParentSessionID string `json:"parent_id"`
	}
	for i := range childSessions {
		if childSessions[i].Title == "child" {
			child = &childSessions[i]
			break
		}
	}
	if child == nil {
		t.Fatalf("child session missing from list --json: %s", childListOut)
	}
	if child.GroupPath != "acme/backend" {
		t.Errorf("child group_path = %q, want %q", child.GroupPath, "acme/backend")
	}
	if child.ParentSessionID != parentID {
		t.Errorf("child parent_session_id = %q, want parent ID %q", child.ParentSessionID, parentID)
	}
}

func worktreeInheritanceEnv(home string, extra ...string) []string {
	env := make([]string, 0, len(os.Environ())+7+len(extra))
	for _, kv := range os.Environ() {
		if strings.HasPrefix(kv, "TMUX") ||
			strings.HasPrefix(kv, "AGENTDECK_") ||
			strings.HasPrefix(kv, "HOME=") ||
			strings.HasPrefix(kv, "XDG_CONFIG_HOME=") ||
			strings.HasPrefix(kv, "XDG_DATA_HOME=") ||
			strings.HasPrefix(kv, "XDG_CACHE_HOME=") {
			continue
		}
		env = append(env, kv)
	}
	env = append(env,
		"HOME="+home,
		"XDG_CONFIG_HOME="+filepath.Join(home, ".config"),
		"XDG_DATA_HOME="+filepath.Join(home, ".local", "share"),
		"XDG_CACHE_HOME="+filepath.Join(home, ".cache"),
		"AGENTDECK_PROFILE=worktree_group_inheritance_test",
		"TERM=dumb",
	)
	return append(env, extra...)
}

func runWorktreeInheritanceCommand(
	t *testing.T,
	home string,
	dir string,
	extraEnv []string,
	name string,
	args ...string,
) string {
	t.Helper()
	cmd := exec.Command(name, args...)
	cmd.Dir = dir
	cmd.Env = worktreeInheritanceEnv(home, extraEnv...)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("%s %v: %v\noutput: %s", name, args, err, out)
	}
	return string(out)
}
