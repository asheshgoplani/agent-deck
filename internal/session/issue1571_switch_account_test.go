package session

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// Tests for #1571: switch-account on a pre-account-tracking session (empty
// account field) resolved a wrong/same source config dir, declared "nothing
// to migrate", and the restarted `claude --resume` died with "No conversation
// found". LocateConversationConfigDir makes the DISK authoritative; the
// subagent dir migrates alongside the jsonl; VerifyConversationInDir gates
// the account flip.

func locateTestConfig(profiles map[string]string) *UserConfig {
	cfg := &UserConfig{Profiles: map[string]ProfileSettings{}}
	for name, dir := range profiles {
		cfg.Profiles[name] = ProfileSettings{Claude: ProfileClaudeSettings{ConfigDir: dir}}
	}
	return cfg
}

// TestLocateConversationConfigDir_FindsAcrossProfiles is the core #1571
// repro: the session's account field is empty, the resolver answers the
// TARGET dir (so src == dst reads as "nothing to migrate"), but the real
// conversation lives in another profile's config dir.
func TestLocateConversationConfigDir_FindsAcrossProfiles(t *testing.T) {
	realSrc, target, project := t.TempDir(), t.TempDir(), t.TempDir()
	cfg := locateTestConfig(map[string]string{"buddii": realSrc, "seminno": target})

	inst := migTestInstance(t, project)
	inst.Account = "" // pre-account-tracking session
	writeConversation(t, realSrc, project, migTestSID, migTestLines)

	// Old behavior this locks out: resolver says target, src == dst, no-op.
	if migrated, err := MigrateConversationFrom(inst, target, target); err != nil || migrated != "" {
		t.Fatalf("precondition: src==dst should be a silent no-op, got (%q, %v)", migrated, err)
	}

	dir, sid, size := LocateConversationConfigDir(cfg, inst, target)
	if dir != realSrc {
		t.Fatalf("LocateConversationConfigDir = %q, want %q", dir, realSrc)
	}
	if sid != migTestSID {
		t.Fatalf("sessionID = %q, want %q", sid, migTestSID)
	}
	if size != int64(len(migTestLines)) {
		t.Fatalf("size = %d, want %d", size, len(migTestLines))
	}

	// The located dir migrates cleanly into the target.
	migrated, err := MigrateConversationFrom(inst, dir, target)
	if err != nil {
		t.Fatalf("MigrateConversationFrom: %v", err)
	}
	if migrated == "" {
		t.Fatal("expected a migrated path, got no-op")
	}
	if err := VerifyConversationInDir(inst, target, size); err != nil {
		t.Fatalf("VerifyConversationInDir after migration: %v", err)
	}
}

// TestLocateConversationConfigDir_SkipsTinyStub: a poisoned stub (the ~646B
// jsonl a failed restart writes at the resume path) must lose to the real
// conversation regardless of candidate order.
func TestLocateConversationConfigDir_SkipsTinyStub(t *testing.T) {
	stubDir, realDir, project := t.TempDir(), t.TempDir(), t.TempDir()
	cfg := locateTestConfig(map[string]string{"a-stub": stubDir, "b-real": realDir})

	inst := migTestInstance(t, project)
	writeConversation(t, stubDir, project, migTestSID, strings.Repeat("x", 646))
	realContent := strings.Repeat(migTestLines, 100)
	writeConversation(t, realDir, project, migTestSID, realContent)

	// stubDir passed as the first (resolver) candidate: largest still wins.
	dir, _, size := LocateConversationConfigDir(cfg, inst, stubDir)
	if dir != realDir {
		t.Fatalf("LocateConversationConfigDir picked stub dir %q, want %q", dir, realDir)
	}
	if size != int64(len(realContent)) {
		t.Fatalf("size = %d, want %d", size, len(realContent))
	}
}

func TestLocateConversationConfigDir_NotFound(t *testing.T) {
	cfg := locateTestConfig(map[string]string{"work": t.TempDir()})
	inst := migTestInstance(t, t.TempDir())
	dir, sid, size := LocateConversationConfigDir(cfg, inst, t.TempDir())
	if dir != "" || sid != "" || size != 0 {
		t.Fatalf("expected not-found, got (%q, %q, %d)", dir, sid, size)
	}
}

// TestLocateConversationConfigDir_NoSessionIDUsesNewest: with no stored id,
// the newest UUID-named conversation across all candidate dirs wins.
func TestLocateConversationConfigDir_NoSessionIDUsesNewest(t *testing.T) {
	oldDir, newDir, project := t.TempDir(), t.TempDir(), t.TempDir()
	cfg := locateTestConfig(map[string]string{"old": oldDir, "new": newDir})

	inst := migTestInstance(t, project)
	inst.ClaudeSessionID = ""

	oldFile := writeConversation(t, oldDir, project, migTestSID, "old\n")
	newFile := writeConversation(t, newDir, project, migTestSID2, "newer conversation\n")
	now := time.Now()
	if err := os.Chtimes(oldFile, now.Add(-2*time.Hour), now.Add(-2*time.Hour)); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(newFile, now, now); err != nil {
		t.Fatal(err)
	}

	dir, sid, size := LocateConversationConfigDir(cfg, inst)
	if dir != newDir {
		t.Fatalf("LocateConversationConfigDir = %q, want newest dir %q", dir, newDir)
	}
	if sid != migTestSID2 {
		t.Fatalf("sessionID = %q, want %q", sid, migTestSID2)
	}
	if size != int64(len("newer conversation\n")) {
		t.Fatalf("size = %d", size)
	}
}

// TestMigrateConversationFrom_MigratesSubagentDir: the companion
// projects/<enc>/<sid>/ subagent-transcript directory travels with the jsonl.
func TestMigrateConversationFrom_MigratesSubagentDir(t *testing.T) {
	src, dst, project := t.TempDir(), t.TempDir(), t.TempDir()
	inst := migTestInstance(t, project)
	writeConversation(t, src, project, migTestSID, migTestLines)

	enc := ConvertToClaudeDirName(project)
	subSrc := filepath.Join(src, "projects", enc, migTestSID, "nested")
	if err := os.MkdirAll(subSrc, 0o700); err != nil {
		t.Fatal(err)
	}
	agentContent := "{\"type\":\"agent\"}\n"
	if err := os.WriteFile(filepath.Join(subSrc, "agent-1.jsonl"), []byte(agentContent), 0o600); err != nil {
		t.Fatal(err)
	}

	if _, err := MigrateConversationFrom(inst, src, dst); err != nil {
		t.Fatalf("MigrateConversationFrom: %v", err)
	}

	migratedAgent := filepath.Join(dst, "projects", enc, migTestSID, "nested", "agent-1.jsonl")
	got, err := os.ReadFile(migratedAgent)
	if err != nil {
		t.Fatalf("subagent transcript not migrated: %v", err)
	}
	if string(got) != agentContent {
		t.Fatalf("subagent transcript content = %q, want %q", got, agentContent)
	}
	// Copy-only: source keeps its subagent dir.
	if _, err := os.Stat(filepath.Join(subSrc, "agent-1.jsonl")); err != nil {
		t.Fatalf("source subagent transcript touched: %v", err)
	}
}

// TestMigrateConversationFrom_OverwritesStubDestination: a poisoned stub at
// the target resume path is replaced by the real conversation, with a .bak-
// snapshot kept (a plain cp -n would silently keep the stub — the manual
// recovery in #1571 hit exactly that).
func TestMigrateConversationFrom_OverwritesStubDestination(t *testing.T) {
	src, dst, project := t.TempDir(), t.TempDir(), t.TempDir()
	inst := migTestInstance(t, project)
	realContent := strings.Repeat(migTestLines, 50)
	writeConversation(t, src, project, migTestSID, realContent)
	writeConversation(t, dst, project, migTestSID, strings.Repeat("s", 646))

	migrated, err := MigrateConversationFrom(inst, src, dst)
	if err != nil {
		t.Fatalf("MigrateConversationFrom: %v", err)
	}
	got, err := os.ReadFile(migrated)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != realContent {
		t.Fatal("stub not replaced by real conversation")
	}
	baks, err := filepath.Glob(migrated + ".bak-*")
	if err != nil || len(baks) != 1 {
		t.Fatalf("expected exactly one stub backup, got %v (err %v)", baks, err)
	}
}

func TestVerifyConversationInDir(t *testing.T) {
	cfgDir, project := t.TempDir(), t.TempDir()
	inst := migTestInstance(t, project)

	if err := VerifyConversationInDir(inst, cfgDir, 0); err == nil {
		t.Fatal("expected error for missing conversation")
	}

	content := strings.Repeat("a", 1000)
	writeConversation(t, cfgDir, project, migTestSID, content)

	if err := VerifyConversationInDir(inst, cfgDir, 0); err != nil {
		t.Fatalf("existence-only verify: %v", err)
	}
	if err := VerifyConversationInDir(inst, cfgDir, 1000); err != nil {
		t.Fatalf("exact-size verify: %v", err)
	}
	if err := VerifyConversationInDir(inst, cfgDir, 995); err != nil {
		t.Fatalf("within-1%%-band verify: %v", err)
	}
	if err := VerifyConversationInDir(inst, cfgDir, 646); err == nil {
		t.Fatal("expected size-mismatch error for stub-sized expectation")
	}

	fresh := migTestInstance(t, project)
	fresh.ClaudeSessionID = ""
	if err := VerifyConversationInDir(fresh, cfgDir, 0); err == nil {
		t.Fatal("expected error when no session id to verify")
	}
}
