package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// sessionMoveAddSession is a helper that creates a test session and returns
// its resolved ID. All moves-tests start from the same setup: one claude
// session pointing at home/old-proj, seeded Claude session history in
// ~/.claude/projects/<old-encoded>/.
func sessionMoveAddSession(t *testing.T, home, oldPath, title string) string {
	t.Helper()
	if err := os.MkdirAll(oldPath, 0o755); err != nil {
		t.Fatalf("mkdir old path: %v", err)
	}
	stdout, stderr, code := runAgentDeck(t, home,
		"add",
		"-t", title,
		"-c", "claude",
		"--no-parent",
		"--json",
		oldPath,
	)
	if code != 0 {
		t.Fatalf("agent-deck add failed (exit %d)\nstdout: %s\nstderr: %s", code, stdout, stderr)
	}
	var resp struct {
		ID string `json:"id"`
	}
	if err := json.Unmarshal([]byte(stdout), &resp); err != nil {
		t.Fatalf("parse add response: %v\nstdout: %s", err, stdout)
	}
	if resp.ID == "" {
		t.Fatalf("add returned empty id; stdout: %s", stdout)
	}
	return resp.ID
}

// claudeProjectSlugForTest mirrors internal/costs.slugifyProjectPath so we
// can seed + assert the migration target. Claude encodes / and . as -.
func claudeProjectSlugForTest(projectPath string) string {
	projectPath = strings.TrimRight(projectPath, "/")
	slug := strings.ReplaceAll(projectPath, "/", "-")
	slug = strings.ReplaceAll(slug, ".", "-")
	return slug
}

// seedClaudeProjectDir seeds ~/.claude/projects/<slug-of-projectPath>/ with
// a sentinel file so we can detect whether it moved.
func seedClaudeProjectDir(t *testing.T, home, projectPath, sentinel string) string {
	t.Helper()
	slug := claudeProjectSlugForTest(projectPath)
	projectsDir := filepath.Join(home, ".claude", "projects", slug)
	if err := os.MkdirAll(projectsDir, 0o755); err != nil {
		t.Fatalf("mkdir claude project dir: %v", err)
	}
	sentinelPath := filepath.Join(projectsDir, "abc-123.jsonl")
	if err := os.WriteFile(sentinelPath, []byte(sentinel), 0o644); err != nil {
		t.Fatalf("write sentinel: %v", err)
	}
	return projectsDir
}

// TestSessionMove_UpdatesPath asserts the most basic contract: the CLI
// accepts `session move <id> <new-path>` and persists the new path.
//
// On main this fails with: `Error: unknown session command: move`.
func TestSessionMove_UpdatesPath(t *testing.T) {
	if testing.Short() {
		t.Skip("subprocess CLI test skipped in short mode")
	}
	home := t.TempDir()
	oldPath := filepath.Join(home, "old-proj")
	newPath := filepath.Join(home, "new-proj")
	if err := os.MkdirAll(newPath, 0o755); err != nil {
		t.Fatal(err)
	}

	id := sessionMoveAddSession(t, home, oldPath, "move-basic")

	stdout, stderr, code := runAgentDeck(t, home,
		"session", "move", id, newPath,
		"--no-restart",
		"--json",
	)
	if code != 0 {
		t.Fatalf("agent-deck session move failed (exit %d) — feature missing on main\nstdout: %s\nstderr: %s", code, stdout, stderr)
	}

	listJSON := readSessionsJSON(t, home)
	if !strings.Contains(listJSON, newPath) {
		t.Errorf("session path did not update to %q; list:\n%s", newPath, listJSON)
	}
	if strings.Contains(listJSON, oldPath) {
		t.Errorf("session path still contains old path %q; list:\n%s", oldPath, listJSON)
	}
}

// TestSessionMove_MigratesClaudeProjectDir asserts the value-add over plain
// `session set path`: ~/.claude/projects/<old-slug>/ is moved to the new
// slug so `claude --resume` in the new path picks up history.
func TestSessionMove_MigratesClaudeProjectDir(t *testing.T) {
	if testing.Short() {
		t.Skip("subprocess CLI test skipped in short mode")
	}
	home := t.TempDir()
	oldPath := filepath.Join(home, "src", "proj-v1")
	newPath := filepath.Join(home, "src", "proj-v2")
	if err := os.MkdirAll(newPath, 0o755); err != nil {
		t.Fatal(err)
	}

	id := sessionMoveAddSession(t, home, oldPath, "move-migrate")
	oldClaudeDir := seedClaudeProjectDir(t, home, oldPath, "turn-1\nturn-2\n")
	newClaudeDir := filepath.Join(home, ".claude", "projects", claudeProjectSlugForTest(newPath))

	stdout, stderr, code := runAgentDeck(t, home,
		"session", "move", id, newPath,
		"--no-restart",
		"--json",
	)
	if code != 0 {
		t.Fatalf("session move failed (exit %d)\nstdout: %s\nstderr: %s", code, stdout, stderr)
	}

	if _, err := os.Stat(oldClaudeDir); !os.IsNotExist(err) {
		t.Errorf("old claude project dir still exists at %s (should be migrated)", oldClaudeDir)
	}
	if _, err := os.Stat(newClaudeDir); err != nil {
		t.Errorf("new claude project dir missing at %s: %v", newClaudeDir, err)
	}
	sentinel := filepath.Join(newClaudeDir, "abc-123.jsonl")
	data, err := os.ReadFile(sentinel)
	if err != nil {
		t.Fatalf("sentinel not migrated: %v", err)
	}
	if string(data) != "turn-1\nturn-2\n" {
		t.Errorf("sentinel contents changed: %q", data)
	}
}

// TestSessionMove_CopyFlagPreservesOldDir — with --copy, the old Claude
// projects dir is preserved (useful when multiple sessions share history).
func TestSessionMove_CopyFlagPreservesOldDir(t *testing.T) {
	if testing.Short() {
		t.Skip("subprocess CLI test skipped in short mode")
	}
	home := t.TempDir()
	oldPath := filepath.Join(home, "shared")
	newPath := filepath.Join(home, "forked")
	if err := os.MkdirAll(newPath, 0o755); err != nil {
		t.Fatal(err)
	}

	id := sessionMoveAddSession(t, home, oldPath, "move-copy")
	oldClaudeDir := seedClaudeProjectDir(t, home, oldPath, "shared-history\n")
	newClaudeDir := filepath.Join(home, ".claude", "projects", claudeProjectSlugForTest(newPath))

	stdout, stderr, code := runAgentDeck(t, home,
		"session", "move", id, newPath,
		"--no-restart",
		"--copy",
		"--json",
	)
	if code != 0 {
		t.Fatalf("session move --copy failed (exit %d)\nstdout: %s\nstderr: %s", code, stdout, stderr)
	}

	if _, err := os.Stat(oldClaudeDir); err != nil {
		t.Errorf("--copy must preserve old claude dir, got: %v", err)
	}
	if _, err := os.Stat(newClaudeDir); err != nil {
		t.Errorf("--copy must create new claude dir, got: %v", err)
	}
	sentinelNew, err := os.ReadFile(filepath.Join(newClaudeDir, "abc-123.jsonl"))
	if err != nil {
		t.Fatalf("sentinel not copied to new: %v", err)
	}
	if string(sentinelNew) != "shared-history\n" {
		t.Errorf("new sentinel contents wrong: %q", sentinelNew)
	}
}

// TestSessionMove_GroupFlag asserts --group also moves the session into a
// new group in one shot.
func TestSessionMove_GroupFlag(t *testing.T) {
	if testing.Short() {
		t.Skip("subprocess CLI test skipped in short mode")
	}
	home := t.TempDir()
	oldPath := filepath.Join(home, "proj")
	newPath := filepath.Join(home, "proj-moved")
	if err := os.MkdirAll(newPath, 0o755); err != nil {
		t.Fatal(err)
	}

	id := sessionMoveAddSession(t, home, oldPath, "move-group")

	stdout, stderr, code := runAgentDeck(t, home,
		"session", "move", id, newPath,
		"--group", "work/frontend",
		"--no-restart",
		"--json",
	)
	if code != 0 {
		t.Fatalf("session move --group failed (exit %d)\nstdout: %s\nstderr: %s", code, stdout, stderr)
	}

	listJSON := readSessionsJSON(t, home)
	// The group layer sanitizes path separators (`/` → `-`); accept either form.
	if !strings.Contains(listJSON, "work/frontend") && !strings.Contains(listJSON, "work-frontend") {
		t.Errorf("session did not move to work/frontend group; list:\n%s", listJSON)
	}
}

// TestSessionMove_MissingArguments — helpful error when no path given.
func TestSessionMove_MissingArguments(t *testing.T) {
	if testing.Short() {
		t.Skip("subprocess CLI test skipped in short mode")
	}
	home := t.TempDir()
	oldPath := filepath.Join(home, "proj")
	id := sessionMoveAddSession(t, home, oldPath, "move-err")

	_, stderr, code := runAgentDeck(t, home,
		"session", "move", id,
	)
	if code == 0 {
		t.Errorf("session move with no path should fail")
	}
	combined := strings.ToLower(stderr)
	if !strings.Contains(combined, "path") && !strings.Contains(combined, "usage") {
		t.Errorf("error message should mention path or usage; got: %s", stderr)
	}
}
