package session

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// The migration and restore paths build filenames from session state (the
// stored native session id and the encoded project path). A value that is not
// a single path segment must be refused before any filesystem access, so it
// can never select or write a file outside the account's project directory.

func TestMigrateConversationFrom_RejectsTraversingSessionID(t *testing.T) {
	src := t.TempDir()
	dst := t.TempDir()
	project := filepath.Join(t.TempDir(), "project")
	if err := os.MkdirAll(filepath.Join(src, "projects", ConvertToClaudeDirName(project)), 0o700); err != nil {
		t.Fatal(err)
	}
	// Without the guard this resolves to <src>/evil.jsonl and is copied to
	// <dst>/evil.jsonl, outside both project dirs.
	if err := os.WriteFile(filepath.Join(src, "evil.jsonl"), []byte(migTestLines), 0o600); err != nil {
		t.Fatal(err)
	}
	inst := migTestInstance(t, project)
	inst.ClaudeSessionID = "../../evil"

	migrated, err := MigrateConversationFrom(inst, src, dst)
	if err == nil || !strings.Contains(err.Error(), "not a single path segment") {
		t.Fatalf("MigrateConversationFrom(traversing id) = %q, %v; want refusal", migrated, err)
	}
	if _, statErr := os.Lstat(filepath.Join(dst, "evil.jsonl")); !os.IsNotExist(statErr) {
		t.Fatalf("traversing id wrote outside the project dir: %v", statErr)
	}
	if inst.ClaudeSessionID != "../../evil" {
		t.Fatalf("refusal must not replace the stored id, got %q", inst.ClaudeSessionID)
	}
}

func TestRestoreOrphanedConversationBackup_RejectsTraversingSessionID(t *testing.T) {
	cfg := t.TempDir()
	project := filepath.Join(t.TempDir(), "project")
	if err := os.MkdirAll(filepath.Join(cfg, "projects", ConvertToClaudeDirName(project)), 0o700); err != nil {
		t.Fatal(err)
	}
	// Without the guard the "live" path resolves to <cfg>/evil.jsonl and this
	// orphan is renamed onto it.
	if err := os.WriteFile(filepath.Join(cfg, "evil.jsonl.bak-1"), []byte(migTestLines), 0o600); err != nil {
		t.Fatal(err)
	}
	inst := migTestInstance(t, project)
	inst.ClaudeSessionID = "../../evil"

	restored, err := RestoreOrphanedConversationBackup(inst, cfg)
	if err == nil || !strings.Contains(err.Error(), "not a single path segment") {
		t.Fatalf("RestoreOrphanedConversationBackup(traversing id) = %q, %v; want refusal", restored, err)
	}
	if _, statErr := os.Lstat(filepath.Join(cfg, "evil.jsonl")); !os.IsNotExist(statErr) {
		t.Fatalf("traversing id restored outside the project dir: %v", statErr)
	}
	if _, statErr := os.Lstat(filepath.Join(cfg, "evil.jsonl.bak-1")); statErr != nil {
		t.Fatalf("orphan must be left untouched: %v", statErr)
	}
}

func TestContainedConversationPath(t *testing.T) {
	root := t.TempDir()
	want := filepath.Join(root, "projects", "-tmp-project", migTestSID+".jsonl")
	got, err := containedConversationPath(root, "projects", "-tmp-project", migTestSID+".jsonl")
	if err != nil || got != want {
		t.Fatalf("valid components = %q, %v; want %q", got, err, want)
	}
	for _, component := range []string{"", ".", "..", "../x", "x/..", "a/b", `a\b`, "/abs", "x\x00y", "..hidden"} {
		if got, err := containedConversationPath(root, "projects", component); err == nil {
			t.Fatalf("component %q accepted as %q", component, got)
		}
	}
}
