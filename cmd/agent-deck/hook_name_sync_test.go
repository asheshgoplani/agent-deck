package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"github.com/asheshgoplani/agent-deck/internal/session"
)

// writeClaudeSessionFile seeds ~/.claude/sessions/<pid>.json with the given
// payload. The PID is used only as the filename — the matching happens by
// sessionId inside the file.
func writeClaudeSessionFile(t *testing.T, claudeDir string, pid int, payload map[string]any) {
	t.Helper()
	sessionsDir := filepath.Join(claudeDir, "sessions")
	if err := os.MkdirAll(sessionsDir, 0o755); err != nil {
		t.Fatalf("mkdir sessions: %v", err)
	}
	b, err := json.Marshal(payload)
	if err != nil {
		t.Fatalf("marshal session payload: %v", err)
	}
	fn := filepath.Join(sessionsDir, fmt.Sprintf("%d.json", pid))
	if err := os.WriteFile(fn, b, 0o644); err != nil {
		t.Fatalf("write session file: %v", err)
	}
}

// TestFindClaudeSessionName_MatchBySessionID asserts the helper scans
// ~/.claude/sessions/*.json and returns the `name` field for the matching
// sessionId. This is the core lookup primitive for #572.
func TestFindClaudeSessionName_MatchBySessionID(t *testing.T) {
	claudeDir := t.TempDir()

	writeClaudeSessionFile(t, claudeDir, 99999, map[string]any{
		"pid":       99999,
		"sessionId": "sid-123",
		"cwd":       "/home/user/proj",
		"name":      "my-feature-branch",
	})
	writeClaudeSessionFile(t, claudeDir, 88888, map[string]any{
		"pid":       88888,
		"sessionId": "other-id",
		"name":      "unrelated",
	})

	got := findClaudeSessionName(claudeDir, "sid-123")
	if got != "my-feature-branch" {
		t.Errorf("findClaudeSessionName = %q, want %q", got, "my-feature-branch")
	}
}

// TestFindClaudeSessionName_NoMatch returns empty when sessionId is unknown.
func TestFindClaudeSessionName_NoMatch(t *testing.T) {
	claudeDir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(claudeDir, "sessions"), 0o755); err != nil {
		t.Fatal(err)
	}
	if got := findClaudeSessionName(claudeDir, "nonexistent"); got != "" {
		t.Errorf("findClaudeSessionName no-match = %q, want empty", got)
	}
}

// TestFindClaudeSessionName_EmptyNameField returns empty string (i.e. no sync)
// when the matching session has an empty or missing `name` field — sessions
// started without --name must not stomp on agent-deck's auto-generated title.
func TestFindClaudeSessionName_EmptyNameField(t *testing.T) {
	claudeDir := t.TempDir()
	writeClaudeSessionFile(t, claudeDir, 111, map[string]any{
		"pid":       111,
		"sessionId": "sid-empty",
	})
	writeClaudeSessionFile(t, claudeDir, 222, map[string]any{
		"pid":       222,
		"sessionId": "sid-explicit-empty",
		"name":      "",
	})
	if got := findClaudeSessionName(claudeDir, "sid-empty"); got != "" {
		t.Errorf("findClaudeSessionName missing-name = %q, want empty", got)
	}
	if got := findClaudeSessionName(claudeDir, "sid-explicit-empty"); got != "" {
		t.Errorf("findClaudeSessionName explicit-empty-name = %q, want empty", got)
	}
}

// TestFindClaudeSessionName_MissingSessionsDir returns empty and does not
// error when the Claude sessions dir doesn't exist (e.g. fresh install).
func TestFindClaudeSessionName_MissingSessionsDir(t *testing.T) {
	claudeDir := t.TempDir() // no sessions/ subdir
	if got := findClaudeSessionName(claudeDir, "anything"); got != "" {
		t.Errorf("findClaudeSessionName missing-dir = %q, want empty", got)
	}
}

// TestApplyClaudeTitleSync_UpdatesInstance is the integration test for #572:
// when the Claude session metadata has a `name` that differs from the
// agent-deck session title, applyClaudeTitleSync must update the title in
// storage.
func TestApplyClaudeTitleSync_UpdatesInstance(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("AGENTDECK_PROFILE", "sync_test_572")

	claudeDir := filepath.Join(home, ".claude")
	sid := "abc-def-0001"
	writeClaudeSessionFile(t, claudeDir, 42, map[string]any{
		"pid":       42,
		"sessionId": sid,
		"cwd":       filepath.Join(home, "proj"),
		"name":      "renamed-by-user",
	})

	storage, err := session.NewStorageWithProfile("sync_test_572")
	if err != nil {
		t.Fatalf("new storage: %v", err)
	}
	t.Cleanup(func() { _ = storage.Close() })

	projectDir := filepath.Join(home, "proj")
	if err := os.MkdirAll(projectDir, 0o755); err != nil {
		t.Fatal(err)
	}
	inst := &session.Instance{
		ID:          "inst-A",
		Title:       "rustic-island",
		Tool:        "claude",
		ProjectPath: projectDir,
		Command:     "claude",
	}
	if err := storage.Save([]*session.Instance{inst}); err != nil {
		t.Fatalf("seed save: %v", err)
	}

	applyClaudeTitleSync("inst-A", sid)

	loaded, err := storage.Load()
	if err != nil {
		t.Fatalf("reload: %v", err)
	}
	var found *session.Instance
	for _, i := range loaded {
		if i.ID == "inst-A" {
			found = i
			break
		}
	}
	if found == nil {
		t.Fatal("instance disappeared")
	}
	if found.Title != "renamed-by-user" {
		t.Errorf("post-sync Title = %q, want %q (#572)", found.Title, "renamed-by-user")
	}
}

// TestApplyClaudeTitleSync_NoopWhenNameMissing guarantees we don't touch
// sessions that Claude doesn't have a user-assigned name for — preserving
// the existing adjective-noun agent-deck title.
func TestApplyClaudeTitleSync_NoopWhenNameMissing(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("AGENTDECK_PROFILE", "sync_test_572_noop")

	storage, err := session.NewStorageWithProfile("sync_test_572_noop")
	if err != nil {
		t.Fatalf("new storage: %v", err)
	}
	t.Cleanup(func() { _ = storage.Close() })

	projectDir := filepath.Join(home, "proj")
	if err := os.MkdirAll(projectDir, 0o755); err != nil {
		t.Fatal(err)
	}
	inst := &session.Instance{
		ID:          "inst-B",
		Title:       "rustic-island",
		Tool:        "claude",
		ProjectPath: projectDir,
		Command:     "claude",
	}
	if err := storage.Save([]*session.Instance{inst}); err != nil {
		t.Fatalf("seed save: %v", err)
	}

	applyClaudeTitleSync("inst-B", "no-such-sid")

	loaded, err := storage.Load()
	if err != nil {
		t.Fatal(err)
	}
	for _, i := range loaded {
		if i.ID == "inst-B" && i.Title != "rustic-island" {
			t.Errorf("Title = %q, want unchanged 'rustic-island'", i.Title)
		}
	}
}

// TestApplyClaudeTitleSync_NoopWhenNameEqualsTitle avoids redundant writes.
// We use the DB-level last-modified timestamp (not filesystem mtime, which
// can tick for unrelated reasons like WAL rollover on Open).
func TestApplyClaudeTitleSync_NoopWhenNameEqualsTitle(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("AGENTDECK_PROFILE", "sync_test_572_equal")

	claudeDir := filepath.Join(home, ".claude")
	sid := "sid-eq"
	writeClaudeSessionFile(t, claudeDir, 7, map[string]any{
		"pid":       7,
		"sessionId": sid,
		"name":      "already-set",
	})

	storage, err := session.NewStorageWithProfile("sync_test_572_equal")
	if err != nil {
		t.Fatalf("new storage: %v", err)
	}
	t.Cleanup(func() { _ = storage.Close() })

	projectDir := filepath.Join(home, "proj")
	if err := os.MkdirAll(projectDir, 0o755); err != nil {
		t.Fatal(err)
	}
	inst := &session.Instance{
		ID:          "inst-C",
		Title:       "already-set",
		Tool:        "claude",
		ProjectPath: projectDir,
		Command:     "claude",
	}
	if err := storage.Save([]*session.Instance{inst}); err != nil {
		t.Fatalf("seed save: %v", err)
	}
	beforeTS, err := storage.GetUpdatedAt()
	if err != nil {
		t.Fatalf("GetUpdatedAt before: %v", err)
	}

	applyClaudeTitleSync("inst-C", sid)

	afterTS, err := storage.GetUpdatedAt()
	if err != nil {
		t.Fatalf("GetUpdatedAt after: %v", err)
	}
	if !afterTS.Equal(beforeTS) {
		t.Errorf("DB last-modified advanced when title already equaled Claude name: before=%v after=%v (redundant write)", beforeTS, afterTS)
	}
}

// TestApplyClaudeTitleSync_NoopWhenTitleLocked (#697): when the user has set
// TitleLocked=true on an instance, Claude renaming the session (e.g. from
// "SCRUM-351" to "auto-refresh-task-lists") must not overwrite the
// agent-deck title. Conductors depend on semantic titles surviving Claude's
// /rename of its own session.
func TestApplyClaudeTitleSync_NoopWhenTitleLocked(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("AGENTDECK_PROFILE", "sync_test_697_lock")

	claudeDir := filepath.Join(home, ".claude")
	sid := "sid-697"
	writeClaudeSessionFile(t, claudeDir, 697, map[string]any{
		"pid":       697,
		"sessionId": sid,
		"name":      "auto-refresh-task-lists",
	})

	storage, err := session.NewStorageWithProfile("sync_test_697_lock")
	if err != nil {
		t.Fatalf("new storage: %v", err)
	}
	t.Cleanup(func() { _ = storage.Close() })

	projectDir := filepath.Join(home, "proj")
	if err := os.MkdirAll(projectDir, 0o755); err != nil {
		t.Fatal(err)
	}
	inst := &session.Instance{
		ID:          "inst-697",
		Title:       "SCRUM-351",
		TitleLocked: true,
		Tool:        "claude",
		ProjectPath: projectDir,
		Command:     "claude",
	}
	if err := storage.Save([]*session.Instance{inst}); err != nil {
		t.Fatalf("seed save: %v", err)
	}

	applyClaudeTitleSync("inst-697", sid)

	loaded, err := storage.Load()
	if err != nil {
		t.Fatalf("reload: %v", err)
	}
	var found *session.Instance
	for _, i := range loaded {
		if i.ID == "inst-697" {
			found = i
			break
		}
	}
	if found == nil {
		t.Fatal("instance disappeared")
	}
	if found.Title != "SCRUM-351" {
		t.Errorf("post-sync Title = %q, want %q (#697 TitleLocked must block sync)", found.Title, "SCRUM-351")
	}
	if !found.TitleLocked {
		t.Errorf("TitleLocked lost across storage round-trip: got false, want true")
	}
}
