package session

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func futureTime(t *testing.T) time.Time {
	t.Helper()
	return time.Now().Add(time.Hour)
}

// Tests for MigrateConversation / MigrateConversationFrom — the
// conversation-follows-account half of the #924 account switch.
// Empirical ground truth (2026-06-10, two real accounts): Claude's
// `--resume <id>` is a pure file lookup under
// <config-dir>/projects/<encoded-path>/<id>.jsonl, and resuming can RENAME
// the file to a fresh UUID, so the stored ClaudeSessionID may be stale.

const (
	migTestSID   = "51d58f67-5c46-437c-bfb4-645d27406c9a"
	migTestSID2  = "479b92a9-19f8-43e4-9d98-fbba200b5820"
	migTestLines = "{\"type\":\"user\",\"sessionId\":\"" + migTestSID + "\"}\n"
)

// migTestInstance builds a minimal Claude instance rooted in a temp project.
func migTestInstance(t *testing.T, projectPath string) *Instance {
	t.Helper()
	return &Instance{
		ID:              "mig-test",
		Title:           "mig-test",
		ProjectPath:     projectPath,
		Tool:            "claude",
		ClaudeSessionID: migTestSID,
	}
}

// writeConversation creates <cfgDir>/projects/<encoded>/<sid>.jsonl.
func writeConversation(t *testing.T, cfgDir, projectPath, sid, content string) string {
	t.Helper()
	dir := filepath.Join(cfgDir, "projects", ConvertToClaudeDirName(projectPath))
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, sid+".jsonl")
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

// writeConversationBackup creates <cfgDir>/projects/<encoded>/<sid>.jsonl.bak-<suffix>.
func writeConversationBackup(t *testing.T, cfgDir, projectPath, sid, suffix, content string) string {
	t.Helper()
	dir := filepath.Join(cfgDir, "projects", ConvertToClaudeDirName(projectPath))
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, sid+".jsonl.bak-"+suffix)
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func resolvedClaudeProjectPath(t *testing.T, projectPath string) string {
	t.Helper()
	resolvedPath := projectPath
	if resolved, err := filepath.EvalSymlinks(projectPath); err == nil {
		resolvedPath = resolved
	}
	return resolvedPath
}

func TestMigrateConversationFrom_HappyPath(t *testing.T) {
	src, dst, project := t.TempDir(), t.TempDir(), t.TempDir()
	inst := migTestInstance(t, project)
	writeConversation(t, src, project, migTestSID, migTestLines)

	migrated, err := MigrateConversationFrom(inst, src, dst)
	if err != nil {
		t.Fatalf("MigrateConversationFrom: %v", err)
	}
	want := filepath.Join(dst, "projects", ConvertToClaudeDirName(project), migTestSID+".jsonl")
	if migrated != want {
		t.Errorf("migrated path = %q, want %q", migrated, want)
	}
	got, err := os.ReadFile(want)
	if err != nil {
		t.Fatalf("destination not readable: %v", err)
	}
	if string(got) != migTestLines {
		t.Errorf("destination content mismatch")
	}
	// Copy-only: source must be untouched.
	srcFile := filepath.Join(src, "projects", ConvertToClaudeDirName(project), migTestSID+".jsonl")
	if b, err := os.ReadFile(srcFile); err != nil || string(b) != migTestLines {
		t.Errorf("source file was modified or removed (err=%v)", err)
	}
}

func TestMigrateConversationFrom_SameDirNoOp(t *testing.T) {
	src, project := t.TempDir(), t.TempDir()
	inst := migTestInstance(t, project)
	writeConversation(t, src, project, migTestSID, migTestLines)

	migrated, err := MigrateConversationFrom(inst, src, src)
	if err != nil {
		t.Fatalf("same-dir migration should be a silent no-op, got %v", err)
	}
	if migrated != "" {
		t.Errorf("no-op should return empty path, got %q", migrated)
	}
}

func TestMigrateConversationFrom_SameRealDirViaSymlinkNoOp(t *testing.T) {
	realCfg, linkParent, project := t.TempDir(), t.TempDir(), t.TempDir()
	linkCfg := filepath.Join(linkParent, "linked-claude")
	if err := os.Symlink(realCfg, linkCfg); err != nil {
		t.Skipf("symlink unavailable: %v", err)
	}
	inst := migTestInstance(t, project)
	live := writeConversation(t, realCfg, project, migTestSID, migTestLines)

	migrated, err := MigrateConversationFrom(inst, linkCfg, realCfg)
	if err != nil {
		t.Fatalf("same-real-dir migration should be a silent no-op, got %v", err)
	}
	if migrated != "" {
		t.Errorf("no-op should return empty path, got %q", migrated)
	}
	got, err := os.ReadFile(live)
	if err != nil {
		t.Fatalf("live conversation missing after no-op: %v", err)
	}
	if string(got) != migTestLines {
		t.Errorf("live conversation content changed")
	}
	projDir := filepath.Join(realCfg, "projects", ConvertToClaudeDirName(project))
	baks, _ := filepath.Glob(filepath.Join(projDir, migTestSID+".jsonl.bak-*"))
	if len(baks) != 0 {
		t.Fatalf("same-real-dir no-op created backups: %v", baks)
	}
}

func TestMigrateConversationFrom_MissingSourceErrors(t *testing.T) {
	src, dst, project := t.TempDir(), t.TempDir(), t.TempDir()
	inst := migTestInstance(t, project)

	_, err := MigrateConversationFrom(inst, src, dst)
	if err == nil {
		t.Fatal("expected error when no conversation file exists")
	}
	if !errors.Is(err, ErrNoConversation) {
		t.Errorf("error should wrap ErrNoConversation, got: %v", err)
	}
}

func TestMigrateConversationFrom_StaleIDFallsBackToNewest(t *testing.T) {
	// Resume renamed the conversation to a new UUID: the stored
	// ClaudeSessionID file is gone, only the renamed file remains.
	src, dst, project := t.TempDir(), t.TempDir(), t.TempDir()
	inst := migTestInstance(t, project)
	writeConversation(t, src, project, migTestSID2, migTestLines)
	// Distractor that must be ignored by the fallback scan.
	writeConversation(t, src, project, "agent-deadbeef", "agent file\n")

	migrated, err := MigrateConversationFrom(inst, src, dst)
	if err != nil {
		t.Fatalf("MigrateConversationFrom: %v", err)
	}
	if !strings.HasSuffix(migrated, migTestSID2+".jsonl") {
		t.Errorf("expected fallback to newest conversation %s, got %q", migTestSID2, migrated)
	}
	if inst.ClaudeSessionID != migTestSID2 {
		t.Errorf("ClaudeSessionID not updated after fallback: %q", inst.ClaudeSessionID)
	}
}

func TestMigrateConversationFrom_PrefersStoredIDOverNewer(t *testing.T) {
	// Two sessions can share a project dir. When the stored id's file still
	// exists it is THIS session's conversation — a newer sibling file must
	// not be picked up.
	src, dst, project := t.TempDir(), t.TempDir(), t.TempDir()
	inst := migTestInstance(t, project)
	writeConversation(t, src, project, migTestSID, migTestLines)
	other := writeConversation(t, src, project, migTestSID2, "other session\n")
	// Make the sibling strictly newer.
	if err := os.Chtimes(other, futureTime(t), futureTime(t)); err != nil {
		t.Fatal(err)
	}

	migrated, err := MigrateConversationFrom(inst, src, dst)
	if err != nil {
		t.Fatalf("MigrateConversationFrom: %v", err)
	}
	if !strings.HasSuffix(migrated, migTestSID+".jsonl") {
		t.Errorf("expected stored id %s to win, got %q", migTestSID, migrated)
	}
	if inst.ClaudeSessionID != migTestSID {
		t.Errorf("ClaudeSessionID must not change when stored file exists: %q", inst.ClaudeSessionID)
	}
}

func TestMigrateConversationFrom_BacksUpConflictingDestination(t *testing.T) {
	src, dst, project := t.TempDir(), t.TempDir(), t.TempDir()
	inst := migTestInstance(t, project)
	writeConversation(t, src, project, migTestSID, migTestLines)
	writeConversation(t, dst, project, migTestSID, "older divergent copy\n")

	if _, err := MigrateConversationFrom(inst, src, dst); err != nil {
		t.Fatalf("MigrateConversationFrom: %v", err)
	}
	dstDir := filepath.Join(dst, "projects", ConvertToClaudeDirName(project))
	got, err := os.ReadFile(filepath.Join(dstDir, migTestSID+".jsonl"))
	if err != nil || string(got) != migTestLines {
		t.Fatalf("destination should hold migrated content (err=%v)", err)
	}
	baks, _ := filepath.Glob(filepath.Join(dstDir, migTestSID+".jsonl.bak-*"))
	if len(baks) != 1 {
		t.Fatalf("expected exactly one backup of the clobbered file, found %d", len(baks))
	}
	if b, _ := os.ReadFile(baks[0]); string(b) != "older divergent copy\n" {
		t.Errorf("backup does not preserve the previous destination content")
	}
}

func TestMigrateConversationFrom_RestoresDestinationBackupOnCopyFailure(t *testing.T) {
	src, dst, project := t.TempDir(), t.TempDir(), t.TempDir()
	inst := migTestInstance(t, project)
	srcFile := writeConversation(t, src, project, migTestSID, migTestLines)
	dstFile := writeConversation(t, dst, project, migTestSID, "original destination\n")

	if err := os.Chmod(srcFile, 0); err != nil {
		t.Fatal(err)
	}
	defer func() {
		_ = os.Chmod(srcFile, 0o600)
	}()
	if f, err := os.Open(srcFile); err == nil {
		_ = f.Close()
		t.Skip("chmod did not make the source unreadable on this platform")
	}

	if _, err := MigrateConversationFrom(inst, src, dst); err == nil {
		t.Fatal("expected copy failure")
	}
	got, err := os.ReadFile(dstFile)
	if err != nil {
		t.Fatalf("destination was not restored after failed copy: %v", err)
	}
	if string(got) != "original destination\n" {
		t.Fatalf("destination content = %q, want original", string(got))
	}
	baks, _ := filepath.Glob(filepath.Join(filepath.Dir(dstFile), migTestSID+".jsonl.bak-*"))
	if len(baks) != 0 {
		t.Fatalf("failed migration left orphan backups: %v", baks)
	}
}

func TestRestoreOrphanedConversationBackup_RestoresOrphan(t *testing.T) {
	cfgDir, project := t.TempDir(), t.TempDir()
	inst := migTestInstance(t, project)
	claudeProject := resolvedClaudeProjectPath(t, project)
	bak := writeConversationBackup(t, cfgDir, claudeProject, migTestSID, "100", "orphaned backup\n")
	live := filepath.Join(filepath.Dir(bak), migTestSID+".jsonl")

	restored, err := RestoreOrphanedConversationBackup(inst, cfgDir)
	if err != nil {
		t.Fatalf("RestoreOrphanedConversationBackup: %v", err)
	}
	if restored != live {
		t.Fatalf("restored path = %q, want %q", restored, live)
	}
	got, err := os.ReadFile(live)
	if err != nil {
		t.Fatalf("restored live conversation not readable: %v", err)
	}
	if string(got) != "orphaned backup\n" {
		t.Fatalf("restored content = %q, want backup content", string(got))
	}
}

func TestRestoreOrphanedConversationBackup_UsesEffectiveWorkingDirForMultiRepo(t *testing.T) {
	cfgDir, project, multiRepoDir := t.TempDir(), t.TempDir(), t.TempDir()
	inst := migTestInstance(t, project)
	inst.MultiRepoEnabled = true
	inst.MultiRepoTempDir = multiRepoDir

	resolvedMultiRepoDir := resolvedClaudeProjectPath(t, multiRepoDir)
	bak := writeConversationBackup(t, cfgDir, resolvedMultiRepoDir, migTestSID, "100", "multi-repo backup\n")
	live := filepath.Join(filepath.Dir(bak), migTestSID+".jsonl")

	restored, err := RestoreOrphanedConversationBackup(inst, cfgDir)
	if err != nil {
		t.Fatalf("RestoreOrphanedConversationBackup: %v", err)
	}
	if restored != live {
		t.Fatalf("restored path = %q, want %q", restored, live)
	}
	if got, err := os.ReadFile(live); err != nil || string(got) != "multi-repo backup\n" {
		t.Fatalf("restored live conversation content mismatch (err=%v, content=%q)", err, string(got))
	}

	projectLive := filepath.Join(cfgDir, "projects", ConvertToClaudeDirName(project), migTestSID+".jsonl")
	if fileIsRegular(projectLive) {
		t.Fatalf("restore used ProjectPath instead of EffectiveWorkingDir: %s", projectLive)
	}
}

func TestRestoreOrphanedConversationBackup_NewestBackupWinsByMtime(t *testing.T) {
	cfgDir, project := t.TempDir(), t.TempDir()
	inst := migTestInstance(t, project)
	claudeProject := resolvedClaudeProjectPath(t, project)
	older := writeConversationBackup(t, cfgDir, claudeProject, migTestSID, "999", "older backup\n")
	newer := writeConversationBackup(t, cfgDir, claudeProject, migTestSID, "111", "newer backup\n")
	oldTime := time.Now().Add(-time.Hour)
	newTime := time.Now()
	if err := os.Chtimes(older, oldTime, oldTime); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(newer, newTime, newTime); err != nil {
		t.Fatal(err)
	}

	restored, err := RestoreOrphanedConversationBackup(inst, cfgDir)
	if err != nil {
		t.Fatalf("RestoreOrphanedConversationBackup: %v", err)
	}
	if !strings.HasSuffix(restored, migTestSID+".jsonl") {
		t.Fatalf("restored path = %q, want live jsonl", restored)
	}
	got, err := os.ReadFile(restored)
	if err != nil {
		t.Fatalf("restored live conversation not readable: %v", err)
	}
	if string(got) != "newer backup\n" {
		t.Fatalf("restored content = %q, want newest backup content", string(got))
	}
}

func TestRestoreOrphanedConversationBackup_NoOpWhenLivePresent(t *testing.T) {
	cfgDir, project := t.TempDir(), t.TempDir()
	inst := migTestInstance(t, project)
	claudeProject := resolvedClaudeProjectPath(t, project)
	live := writeConversation(t, cfgDir, claudeProject, migTestSID, "live conversation\n")
	writeConversationBackup(t, cfgDir, claudeProject, migTestSID, "100", "stale backup\n")

	restored, err := RestoreOrphanedConversationBackup(inst, cfgDir)
	if err != nil {
		t.Fatalf("RestoreOrphanedConversationBackup: %v", err)
	}
	if restored != "" {
		t.Fatalf("expected no-op with live conversation present, got %q", restored)
	}
	got, err := os.ReadFile(live)
	if err != nil {
		t.Fatalf("live conversation not readable: %v", err)
	}
	if string(got) != "live conversation\n" {
		t.Fatalf("live conversation was overwritten: %q", string(got))
	}
}

func TestMigrateConversationFrom_NonClaudeToolRejected(t *testing.T) {
	src, dst, project := t.TempDir(), t.TempDir(), t.TempDir()
	inst := migTestInstance(t, project)
	inst.Tool = "gemini"

	if _, err := MigrateConversationFrom(inst, src, dst); err == nil {
		t.Fatal("expected error for non-claude tool")
	}
}

func TestMigrateConversation_ResolvesSourceFromInstance(t *testing.T) {
	src, dst, project := t.TempDir(), t.TempDir(), t.TempDir()
	inst := migTestInstance(t, project)
	writeConversation(t, src, project, migTestSID, migTestLines)
	// Instance chain: account/conductor/group unset → env wins.
	t.Setenv("CLAUDE_CONFIG_DIR", src)

	migrated, err := MigrateConversation(inst, dst)
	if err != nil {
		t.Fatalf("MigrateConversation: %v", err)
	}
	if !strings.HasSuffix(migrated, migTestSID+".jsonl") {
		t.Errorf("unexpected migrated path %q", migrated)
	}
}
