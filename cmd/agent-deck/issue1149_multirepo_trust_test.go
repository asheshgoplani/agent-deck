package main

// Regression tests for issue #1149 — multi-repo worktree parent dirs must
// pre-accept the Claude trust dialog and emit a parent CLAUDE.md telling
// Claude which subdirectory to cd into for project commands.
//
// Without these, every multi-repo session greets the user with "do you
// trust this directory?" and Claude then runs build commands at the empty
// parent dir because it has no idea the real repos live one level deeper.
//
// Per @spawnia's spec (gh issue 1149) the fix has two halves:
//
//	if inst.Tool == "claude" {
//	    preAcceptClaudeTrust(parentDir)
//	    writeParentClaudeMD(parentDir, repos)
//	}
//
// invoked inside the multi-repo branch of home.go. The tests below exercise
// the integration entry point session.ApplyMultiRepoClaudeContext directly
// because home.go's session-creation Cmd is not callable from a unit test
// without spinning up the full TUI.

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/asheshgoplani/agent-deck/internal/session"
)

// Happy path — spawning a multi-repo claude session adds a trust entry and
// writes a CLAUDE.md naming every repo subdir.
func TestIssue1149_MultiRepoClaudeSession_PreAcceptsTrustAndWritesParentMD(t *testing.T) {
	dir := t.TempDir()
	claudeJSON := filepath.Join(dir, ".claude.json")
	parentDir := filepath.Join(dir, "multi-repo-worktrees", "feature-x-abcd1234")
	if err := os.MkdirAll(parentDir, 0o755); err != nil {
		t.Fatalf("mkdir parent: %v", err)
	}

	repos := []string{"limes-api", "limes-frontend"}
	if err := session.ApplyMultiRepoClaudeContext("claude", true, claudeJSON, parentDir, repos); err != nil {
		t.Fatalf("ApplyMultiRepoClaudeContext: %v", err)
	}

	// Trust entry created.
	data, err := os.ReadFile(claudeJSON)
	if err != nil {
		t.Fatalf("read claude.json: %v", err)
	}
	var cfg map[string]any
	if err := json.Unmarshal(data, &cfg); err != nil {
		t.Fatalf("unmarshal claude.json: %v", err)
	}
	projects, ok := cfg["projects"].(map[string]any)
	if !ok {
		t.Fatalf("projects key missing or wrong type: %T", cfg["projects"])
	}
	entry, ok := projects[parentDir].(map[string]any)
	if !ok {
		keys := make([]string, 0, len(projects))
		for k := range projects {
			keys = append(keys, k)
		}
		t.Fatalf("parentDir entry missing: keys=%v", keys)
	}
	if entry["hasTrustDialogAccepted"] != true {
		t.Fatalf("hasTrustDialogAccepted: got %v want true", entry["hasTrustDialogAccepted"])
	}

	// Parent CLAUDE.md created and lists every repo subdir.
	mdBytes, err := os.ReadFile(filepath.Join(parentDir, "CLAUDE.md"))
	if err != nil {
		t.Fatalf("read CLAUDE.md: %v", err)
	}
	md := string(mdBytes)
	for _, name := range repos {
		if !strings.Contains(md, name) {
			t.Errorf("CLAUDE.md missing repo %q; got:\n%s", name, md)
		}
	}
	if !strings.Contains(md, "multi-repo") && !strings.Contains(md, "Multi-Repo") {
		t.Errorf("CLAUDE.md does not advertise multi-repo nature; got:\n%s", md)
	}
}

// Boundary — parent CLAUDE.md must enumerate every repo subdir even when
// there are three or more (sanity check that we are not capping at the
// first two).
func TestIssue1149_ParentClaudeMD_ListsAllRepoSubdirs(t *testing.T) {
	parentDir := t.TempDir()
	claudeJSON := filepath.Join(parentDir, ".claude.json")
	repos := []string{"repo-alpha", "repo-beta", "repo-gamma", "repo-delta"}

	if err := session.ApplyMultiRepoClaudeContext("claude", true, claudeJSON, parentDir, repos); err != nil {
		t.Fatalf("ApplyMultiRepoClaudeContext: %v", err)
	}

	md, err := os.ReadFile(filepath.Join(parentDir, "CLAUDE.md"))
	if err != nil {
		t.Fatalf("read CLAUDE.md: %v", err)
	}
	for _, name := range repos {
		if !strings.Contains(string(md), name) {
			t.Errorf("CLAUDE.md missing %q", name)
		}
	}
}

// Failure mode for the integration guard — a non-multi-repo session must
// leave ~/.claude.json untouched and must not produce a CLAUDE.md at the
// project path. Test 3 of @spawnia's spec.
func TestIssue1149_SingleRepoSession_DoesNotModifyClaudeJSON(t *testing.T) {
	dir := t.TempDir()
	claudeJSON := filepath.Join(dir, ".claude.json")
	original := `{"projects":{"/some/unrelated":{"hasTrustDialogAccepted":true}},"someTopLevel":"keep-me"}`
	if err := os.WriteFile(claudeJSON, []byte(original), 0o600); err != nil {
		t.Fatalf("seed claude.json: %v", err)
	}

	parentPath := filepath.Join(dir, "project") // would be inst.ProjectPath in a single-repo session
	if err := os.MkdirAll(parentPath, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}

	// multiRepoEnabled=false → no-op, even though tool is claude.
	if err := session.ApplyMultiRepoClaudeContext("claude", false, claudeJSON, parentPath, []string{"project"}); err != nil {
		t.Fatalf("ApplyMultiRepoClaudeContext: %v", err)
	}

	got, err := os.ReadFile(claudeJSON)
	if err != nil {
		t.Fatalf("read claude.json: %v", err)
	}
	if string(got) != original {
		t.Errorf("claude.json modified for single-repo session.\nwant:\n%s\ngot:\n%s", original, got)
	}
	if _, err := os.Stat(filepath.Join(parentPath, "CLAUDE.md")); !os.IsNotExist(err) {
		t.Errorf("CLAUDE.md should not exist for single-repo session, stat err=%v", err)
	}
}

// Failure mode — preserving existing unrelated entries in ~/.claude.json.
// Test 4 of @spawnia's spec. A bug where we round-tripped via a typed struct
// would silently drop fields we don't model.
func TestIssue1149_PreservesExistingClaudeJSONEntries(t *testing.T) {
	dir := t.TempDir()
	claudeJSON := filepath.Join(dir, ".claude.json")
	original := map[string]any{
		"someTopLevelField":      "keep-me",
		"hasCompletedOnboarding": true,
		"projects": map[string]any{
			"/home/user/projectA": map[string]any{
				"hasTrustDialogAccepted": true,
				"lastSessionId":          "abc-123",
				"customField":            "preserved",
			},
			"/home/user/projectB": map[string]any{
				"hasTrustDialogAccepted": false,
			},
		},
	}
	data, _ := json.Marshal(original)
	if err := os.WriteFile(claudeJSON, data, 0o600); err != nil {
		t.Fatalf("seed claude.json: %v", err)
	}

	parentDir := filepath.Join(dir, "multi-repo-worktrees", "branch-9999aaaa")
	if err := os.MkdirAll(parentDir, 0o755); err != nil {
		t.Fatalf("mkdir parent: %v", err)
	}

	if err := session.ApplyMultiRepoClaudeContext("claude", true, claudeJSON, parentDir, []string{"r1"}); err != nil {
		t.Fatalf("ApplyMultiRepoClaudeContext: %v", err)
	}

	after, err := os.ReadFile(claudeJSON)
	if err != nil {
		t.Fatalf("read claude.json: %v", err)
	}
	var cfg map[string]any
	if err := json.Unmarshal(after, &cfg); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}

	if cfg["someTopLevelField"] != "keep-me" {
		t.Errorf("top-level field dropped: %v", cfg["someTopLevelField"])
	}
	if cfg["hasCompletedOnboarding"] != true {
		t.Errorf("hasCompletedOnboarding dropped: %v", cfg["hasCompletedOnboarding"])
	}

	projects := cfg["projects"].(map[string]any)
	a := projects["/home/user/projectA"].(map[string]any)
	if a["hasTrustDialogAccepted"] != true {
		t.Errorf("projectA.hasTrustDialogAccepted: %v", a["hasTrustDialogAccepted"])
	}
	if a["lastSessionId"] != "abc-123" {
		t.Errorf("projectA.lastSessionId dropped: %v", a["lastSessionId"])
	}
	if a["customField"] != "preserved" {
		t.Errorf("projectA.customField dropped: %v", a["customField"])
	}
	b := projects["/home/user/projectB"].(map[string]any)
	if b["hasTrustDialogAccepted"] != false {
		t.Errorf("projectB.hasTrustDialogAccepted: %v", b["hasTrustDialogAccepted"])
	}
	parentEntry := projects[parentDir].(map[string]any)
	if parentEntry["hasTrustDialogAccepted"] != true {
		t.Errorf("parentDir.hasTrustDialogAccepted: %v", parentEntry["hasTrustDialogAccepted"])
	}
}

// Failure mode — tool != "claude" must be a no-op. Codex / Gemini sessions
// would otherwise corrupt ~/.claude.json with paths they have no business
// touching.
func TestIssue1149_NonClaudeTool_IsNoop(t *testing.T) {
	dir := t.TempDir()
	claudeJSON := filepath.Join(dir, ".claude.json")
	parentDir := filepath.Join(dir, "parent")
	if err := os.MkdirAll(parentDir, 0o755); err != nil {
		t.Fatalf("mkdir parent: %v", err)
	}

	for _, tool := range []string{"codex", "gemini", "copilot", ""} {
		if err := session.ApplyMultiRepoClaudeContext(tool, true, claudeJSON, parentDir, []string{"r1"}); err != nil {
			t.Fatalf("ApplyMultiRepoClaudeContext(tool=%q): %v", tool, err)
		}
		if _, err := os.Stat(claudeJSON); !os.IsNotExist(err) {
			t.Errorf("claude.json created for tool=%q, stat err=%v", tool, err)
		}
		if _, err := os.Stat(filepath.Join(parentDir, "CLAUDE.md")); !os.IsNotExist(err) {
			t.Errorf("CLAUDE.md created for tool=%q, stat err=%v", tool, err)
		}
	}
}

// Idempotence — running the setup twice produces identical state. Worktree
// recreation / session restart commonly re-invokes the path.
func TestIssue1149_Idempotent(t *testing.T) {
	dir := t.TempDir()
	claudeJSON := filepath.Join(dir, ".claude.json")
	parentDir := filepath.Join(dir, "multi-repo-worktrees", "branch-xx")
	if err := os.MkdirAll(parentDir, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	repos := []string{"a", "b"}

	if err := session.ApplyMultiRepoClaudeContext("claude", true, claudeJSON, parentDir, repos); err != nil {
		t.Fatalf("first apply: %v", err)
	}
	first, err := os.ReadFile(claudeJSON)
	if err != nil {
		t.Fatalf("read 1: %v", err)
	}

	if err := session.ApplyMultiRepoClaudeContext("claude", true, claudeJSON, parentDir, repos); err != nil {
		t.Fatalf("second apply: %v", err)
	}
	second, err := os.ReadFile(claudeJSON)
	if err != nil {
		t.Fatalf("read 2: %v", err)
	}

	if string(first) != string(second) {
		t.Errorf("claude.json not idempotent.\nfirst:  %s\nsecond: %s", first, second)
	}
}
