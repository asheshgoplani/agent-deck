package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestSessionMove_GroupSelectorResolvesLikeAdd: `session move --group` must
// resolve its selector the way add/launch/fork do (resolveGroupPathForAdd).
// It used the raw string: "Work/Frontend" created a new top-level group
// "Work-Frontend", and the destination Claude config dir was looked up from
// the raw string, so a config_dir set on the real group was missed and
// history migrated into the default config dir.
func TestSessionMove_GroupSelectorResolvesLikeAdd(t *testing.T) {
	if testing.Short() {
		t.Skip("subprocess CLI test skipped in short mode")
	}
	home := t.TempDir()
	targetDir := filepath.Join(home, ".claude-frontend")
	adDir := filepath.Join(home, ".agent-deck")
	for _, dir := range []string{targetDir, adDir} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	cfg := fmt.Sprintf("[groups.\"work/frontend\".claude]\nconfig_dir = %q\n", targetDir)
	if err := os.WriteFile(filepath.Join(adDir, "config.toml"), []byte(cfg), 0o644); err != nil {
		t.Fatal(err)
	}

	// The target group exists because a session already lives in it.
	sessionMoveAddSession(t, home, filepath.Join(home, "src", "fe-anchor"), "fe-anchor", "--group", "work/frontend")

	oldPath := filepath.Join(home, "src", "resolve-old")
	newPath := filepath.Join(home, "src", "resolve-new")
	if err := os.MkdirAll(newPath, 0o755); err != nil {
		t.Fatal(err)
	}
	id := sessionMoveAddSession(t, home, oldPath, "resolve-me")
	seedClaudeProjectDir(t, home, oldPath, "turn-1\n")

	stdout, stderr, code := runAgentDeck(t, home,
		"session", "move", id, newPath,
		"--group", "Work/Frontend",
		"--no-restart",
		"--json",
	)
	if code != 0 {
		t.Fatalf("session move failed (exit %d)\nstdout: %s\nstderr: %s", code, stdout, stderr)
	}
	var resp struct {
		NewGroup  string `json:"new_group"`
		TargetDir string `json:"target_claude_config_dir"`
	}
	if err := json.Unmarshal([]byte(stdout), &resp); err != nil {
		t.Fatalf("parse move response: %v\nstdout: %s", err, stdout)
	}
	if resp.NewGroup != "work/frontend" {
		t.Errorf("new_group = %q, want %q (selector not resolved like add)", resp.NewGroup, "work/frontend")
	}
	newClaudeDir := filepath.Join(targetDir, "projects", claudeProjectSlugForTest(newPath))
	if _, err := os.Stat(newClaudeDir); err != nil {
		t.Errorf("history not migrated into the resolved group's config dir %s: %v (target_claude_config_dir=%q)", newClaudeDir, err, resp.TargetDir)
	}

	listOut, listErr, listCode := runAgentDeck(t, home, "group", "list", "--json")
	if listCode != 0 {
		t.Fatalf("group list failed (exit %d)\nstdout: %s\nstderr: %s", listCode, listOut, listErr)
	}
	if strings.Contains(listOut, "Work-Frontend") {
		t.Errorf("session move created a stray group Work-Frontend:\n%s", listOut)
	}
}

// TestSessionMove_AmbiguousGroupNameErrors: a bare group name that matches
// several groups must be rejected (as add/launch/fork do), not silently turned
// into a new top-level group.
func TestSessionMove_AmbiguousGroupNameErrors(t *testing.T) {
	if testing.Short() {
		t.Skip("subprocess CLI test skipped in short mode")
	}
	home := t.TempDir()
	sessionMoveAddSession(t, home, filepath.Join(home, "src", "a"), "a", "--group", "work/frontend")
	sessionMoveAddSession(t, home, filepath.Join(home, "src", "b"), "b", "--group", "other/frontend")

	oldPath := filepath.Join(home, "src", "amb-old")
	newPath := filepath.Join(home, "src", "amb-new")
	if err := os.MkdirAll(newPath, 0o755); err != nil {
		t.Fatal(err)
	}
	id := sessionMoveAddSession(t, home, oldPath, "amb")

	stdout, stderr, code := runAgentDeck(t, home,
		"session", "move", id, newPath,
		"--group", "frontend",
		"--no-restart",
		"--json",
	)
	if code == 0 {
		t.Fatalf("session move with ambiguous --group succeeded, want an error\nstdout: %s", stdout)
	}
	if !strings.Contains(stdout+stderr, "ambiguous") {
		t.Errorf("want an 'ambiguous' error, got\nstdout: %s\nstderr: %s", stdout, stderr)
	}
}
