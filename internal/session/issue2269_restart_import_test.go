package session

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// Issue #2269: `session restart` fell back to `--session-id <id>` (a blank
// conversation under a real id) when the transcript lived in another config
// dir. The resume-time chokepoint (conversationIsResumable) must locate it in
// ~/.claude (or any profile's config_dir), copy it into the dir the new
// process will use, and report true so the launcher emits `--resume`.
func TestConversationIsResumable_ImportsTranscriptFromOtherConfigDir(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	accountDir := filepath.Join(home, ".claude-account2")
	t.Setenv("CLAUDE_CONFIG_DIR", accountDir)
	ClearUserConfigCache()
	t.Cleanup(ClearUserConfigCache)

	projectPath := filepath.Join(home, "code", "proj")
	if err := os.MkdirAll(projectPath, 0o755); err != nil {
		t.Fatal(err)
	}
	encoded := ConvertToClaudeDirName(projectPath)
	sessionID := "11111111-2222-3333-4444-555555555555"

	// Canonical transcript only under the default ~/.claude, with a stale
	// history-suppression record from a previous cross-account restart.
	defaultProj := filepath.Join(home, ".claude", "projects", encoded)
	if err := os.MkdirAll(defaultProj, 0o755); err != nil {
		t.Fatal(err)
	}
	keep1 := `{"type":"user","sessionId":"` + sessionID + `","message":{"role":"user","content":"hi"}}` + "\n"
	drop1 := `{"type":"history-suppression","cause":"restored_owner_mismatch","sessionId":"` + sessionID + `"}` + "\n"
	drop2 := `{"type":"bridge-session","sessionId":"` + sessionID + `"}` + "\n"
	keep2 := `{"type":"assistant","sessionId":"` + sessionID + `","message":{"role":"assistant","content":"hello"}}` + "\n"
	src := filepath.Join(defaultProj, sessionID+".jsonl")
	if err := os.WriteFile(src, []byte(keep1+drop1+drop2+keep2), 0o600); err != nil {
		t.Fatal(err)
	}

	inst := NewInstance("restart-import", projectPath)
	inst.Tool = "claude"
	inst.ClaudeSessionID = sessionID

	// The predicate alone is a pure read: the hook/tmux rebind gates call
	// it on live sessions, so it must not copy anything.
	if sessionHasConversationData(inst, sessionID) {
		t.Fatal("predicate must report false without importing")
	}
	if _, err := os.Stat(filepath.Join(accountDir, "projects")); !os.IsNotExist(err) {
		t.Fatal("predicate must not write into the account config dir")
	}

	if !conversationIsResumable(inst, sessionID) {
		t.Fatal("expected true: transcript exists in ~/.claude and must be imported, not replaced by --session-id")
	}

	dst := filepath.Join(accountDir, "projects", encoded, sessionID+".jsonl")
	got, err := os.ReadFile(dst)
	if err != nil {
		t.Fatalf("transcript was not copied into the account config dir: %v", err)
	}
	if string(got) != keep1+keep2 {
		t.Errorf("imported transcript should keep only the conversation records\n got: %q\nwant: %q", got, keep1+keep2)
	}
	if orig, _ := os.ReadFile(src); string(orig) != keep1+drop1+drop2+keep2 {
		t.Error("source transcript must be left untouched (copy-only)")
	}

	// Second call: the file is now in place, no import needed, still true.
	if !conversationIsResumable(inst, sessionID) {
		t.Error("expected true on the second call with the transcript already imported")
	}
}

// The already-poisoned case from the issue: a `--session-id` launch left a
// system-prompt-only stub at the primary path (larger than the real
// transcript), while the real conversation sits in ~/.claude. The stub must
// be replaced (backed up, not lost) by the real transcript.
func TestConversationIsResumable_ReplacesStubWithRealTranscript(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	accountDir := filepath.Join(home, ".claude-account2")
	t.Setenv("CLAUDE_CONFIG_DIR", accountDir)
	ClearUserConfigCache()
	t.Cleanup(ClearUserConfigCache)

	projectPath := filepath.Join(home, "code", "proj")
	if err := os.MkdirAll(projectPath, 0o755); err != nil {
		t.Fatal(err)
	}
	encoded := ConvertToClaudeDirName(projectPath)
	sessionID := "22222222-2222-3333-4444-555555555555"

	real := `{"type":"user","sessionId":"` + sessionID + `","message":{"role":"user","content":"hi"}}` + "\n"
	defaultProj := filepath.Join(home, ".claude", "projects", encoded)
	if err := os.MkdirAll(defaultProj, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(defaultProj, sessionID+".jsonl"), []byte(real), 0o600); err != nil {
		t.Fatal(err)
	}
	// Stub: no "sessionId" anywhere, but much larger than the real file.
	stub := `{"type":"system","subtype":"init","prompt":"` + strings.Repeat("x", 4096) + `"}` + "\n"
	accountProj := filepath.Join(accountDir, "projects", encoded)
	if err := os.MkdirAll(accountProj, 0o755); err != nil {
		t.Fatal(err)
	}
	dst := filepath.Join(accountProj, sessionID+".jsonl")
	if err := os.WriteFile(dst, []byte(stub), 0o600); err != nil {
		t.Fatal(err)
	}

	inst := NewInstance("restart-stub", projectPath)
	inst.Tool = "claude"
	inst.ClaudeSessionID = sessionID

	if !conversationIsResumable(inst, sessionID) {
		t.Fatal("expected true: the real transcript must replace the stub")
	}
	got, _ := os.ReadFile(dst)
	if string(got) != real {
		t.Errorf("primary path should now hold the real transcript, got %q", got)
	}
	baks, _ := filepath.Glob(dst + ".bak-*")
	if len(baks) != 1 {
		t.Errorf("stub should have been backed up, found %v", baks)
	}
}

// The stripped import is always smaller than its source, and Claude appends
// to the primary once it resumes; a larger copy elsewhere is therefore not
// evidence of more history. A primary with conversation data is never
// replaced, even by a larger file.
func TestConversationIsResumable_LargerSourceDoesNotReplacePrimaryWithData(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	accountDir := filepath.Join(home, ".claude-account2")
	t.Setenv("CLAUDE_CONFIG_DIR", accountDir)
	ClearUserConfigCache()
	t.Cleanup(ClearUserConfigCache)

	projectPath := filepath.Join(home, "code", "proj")
	if err := os.MkdirAll(projectPath, 0o755); err != nil {
		t.Fatal(err)
	}
	encoded := ConvertToClaudeDirName(projectPath)
	sessionID := "44444444-2222-3333-4444-555555555555"
	base := `{"type":"user","sessionId":"` + sessionID + `","message":{"role":"user","content":"hi"}}` + "\n"
	newTurn := `{"type":"assistant","sessionId":"` + sessionID + `","message":{"role":"assistant","content":"new turn"}}` + "\n"
	source := base +
		`{"type":"history-suppression","cause":"restored_owner_mismatch","sessionId":"` + sessionID + `","padding":"` + strings.Repeat("x", 512) + `"}` + "\n" +
		`{"type":"bridge-session","sessionId":"` + sessionID + `"}` + "\n"
	accountProj := filepath.Join(accountDir, "projects", encoded)
	defaultProj := filepath.Join(home, ".claude", "projects", encoded)
	for _, d := range []string{accountProj, defaultProj} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	dst := filepath.Join(accountProj, sessionID+".jsonl")
	_ = os.WriteFile(dst, []byte(base+newTurn), 0o600)
	_ = os.WriteFile(filepath.Join(defaultProj, sessionID+".jsonl"), []byte(source), 0o600)

	inst := NewInstance("restart-newer-primary", projectPath)
	inst.Tool = "claude"
	inst.ClaudeSessionID = sessionID

	if !conversationIsResumable(inst, sessionID) {
		t.Fatal("expected true")
	}
	if got, _ := os.ReadFile(dst); string(got) != base+newTurn {
		t.Errorf("primary with data must be authoritative, got %q", got)
	}
	if baks, _ := filepath.Glob(dst + ".bak-*"); len(baks) != 0 {
		t.Errorf("no backup expected, found %v", baks)
	}
}

func TestConversationIsResumable_KeepsPrimaryWhenItHasData(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	accountDir := filepath.Join(home, ".claude-account2")
	t.Setenv("CLAUDE_CONFIG_DIR", accountDir)
	ClearUserConfigCache()
	t.Cleanup(ClearUserConfigCache)

	projectPath := filepath.Join(home, "code", "proj")
	if err := os.MkdirAll(projectPath, 0o755); err != nil {
		t.Fatal(err)
	}
	encoded := ConvertToClaudeDirName(projectPath)
	sessionID := "33333333-2222-3333-4444-555555555555"
	primary := `{"type":"user","sessionId":"` + sessionID + `","message":{"role":"user","content":"newer and longer conversation"}}` + "\n"
	older := `{"type":"user","sessionId":"` + sessionID + `"}` + "\n"
	accountProj := filepath.Join(accountDir, "projects", encoded)
	defaultProj := filepath.Join(home, ".claude", "projects", encoded)
	for _, d := range []string{accountProj, defaultProj} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	dst := filepath.Join(accountProj, sessionID+".jsonl")
	_ = os.WriteFile(dst, []byte(primary), 0o600)
	_ = os.WriteFile(filepath.Join(defaultProj, sessionID+".jsonl"), []byte(older), 0o600)

	inst := NewInstance("restart-keep", projectPath)
	inst.Tool = "claude"
	inst.ClaudeSessionID = sessionID

	if !conversationIsResumable(inst, sessionID) {
		t.Fatal("expected true")
	}
	if got, _ := os.ReadFile(dst); string(got) != primary {
		t.Errorf("primary transcript must be left alone when it is the best copy, got %q", got)
	}
	if baks, _ := filepath.Glob(dst + ".bak-*"); len(baks) != 0 {
		t.Errorf("no backup expected, found %v", baks)
	}
}

func TestSessionHasConversationData_NoImportWhenNowhere(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	accountDir := filepath.Join(home, ".claude-account2")
	t.Setenv("CLAUDE_CONFIG_DIR", accountDir)
	ClearUserConfigCache()
	t.Cleanup(ClearUserConfigCache)

	projectPath := filepath.Join(home, "code", "proj")
	inst := NewInstance("restart-nowhere", projectPath)
	inst.Tool = "claude"
	inst.ClaudeSessionID = "aaaaaaaa-0000-0000-0000-000000000000"

	if conversationIsResumable(inst, inst.ClaudeSessionID) {
		t.Error("expected false when the transcript exists in no config dir")
	}
	if entries, _ := os.ReadDir(filepath.Join(accountDir, "projects")); len(entries) != 0 {
		t.Errorf("nothing should be written when there is nothing to import, got %v", entries)
	}
}

// The scan keys on the instance's bound id; with an id that is not the
// instance's own, LocateConversationConfigDir must not be consulted (its
// empty-id fallback picks the newest sibling conversation).
func TestImportConversationForResume_RequiresBoundID(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	accountDir := filepath.Join(home, ".claude-account2")
	t.Setenv("CLAUDE_CONFIG_DIR", accountDir)
	ClearUserConfigCache()
	t.Cleanup(ClearUserConfigCache)

	projectPath := filepath.Join(home, "code", "proj")
	encoded := ConvertToClaudeDirName(projectPath)
	defaultProj := filepath.Join(home, ".claude", "projects", encoded)
	_ = os.MkdirAll(defaultProj, 0o755)
	_ = os.WriteFile(filepath.Join(defaultProj, "sibling.jsonl"), []byte(`{"type":"user","sessionId":"sibling"}`+"\n"), 0o600)

	inst := NewInstance("unbound", projectPath)
	inst.Tool = "claude"
	importConversationForResume(inst, "some-id")
	if _, err := os.Stat(filepath.Join(accountDir, "projects")); !os.IsNotExist(err) {
		t.Error("nothing may be imported for an id the instance is not bound to")
	}
}

func TestCopyConversationStripped(t *testing.T) {
	dir := t.TempDir()
	src := filepath.Join(dir, "src.jsonl")
	dst := filepath.Join(dir, "dst.jsonl")

	t.Run("drops suppression records and size-checks the remainder", func(t *testing.T) {
		lines := []string{
			`{"type":"summary","leafUuid":"abc"}`,
			`{"type":"history-suppression","cause":"restored_owner_mismatch"}`,
			`{"type":"user","sessionId":"x","text":"history-suppression mentioned in text stays"}`,
			`{"type":"bridge-session","id":"b"}`,
			`{"type":"assistant","sessionId":"x"}`, // no trailing newline
		}
		content := strings.Join(lines, "\n")
		if err := os.WriteFile(src, []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
		written, err := copyConversationStripped(src, dst)
		if err != nil {
			t.Fatal(err)
		}
		want := lines[0] + "\n" + lines[2] + "\n" + lines[4]
		got, _ := os.ReadFile(dst)
		if string(got) != want {
			t.Errorf("got %q\nwant %q", got, want)
		}
		if written != int64(len(want)) {
			t.Errorf("written = %d, want %d", written, len(want))
		}
		info, _ := os.Stat(dst)
		if info.Mode().Perm() != 0o600 {
			t.Errorf("dst mode = %o, want 0600", info.Mode().Perm())
		}
	})

	t.Run("plain transcript copies byte for byte", func(t *testing.T) {
		content := "{\"type\":\"user\",\"sessionId\":\"y\"}\n{\"type\":\"assistant\"}\n"
		if err := os.WriteFile(src, []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
		written, err := copyConversationStripped(src, dst)
		if err != nil {
			t.Fatal(err)
		}
		got, _ := os.ReadFile(dst)
		if string(got) != content || written != int64(len(content)) {
			t.Errorf("got %q (%d bytes), want %q", got, written, content)
		}
	})

	t.Run("failed copy leaves nothing at dst", func(t *testing.T) {
		missing := filepath.Join(dir, "missing.jsonl")
		target := filepath.Join(dir, "never.jsonl")
		if _, err := copyConversationStripped(missing, target); err == nil {
			t.Fatal("expected error for missing source")
		}
		if _, err := os.Stat(target); !os.IsNotExist(err) {
			t.Error("a failed copy must not leave a file at dst")
		}
		if leftovers, _ := filepath.Glob(filepath.Join(dir, "never.jsonl.tmp-*")); len(leftovers) != 0 {
			t.Errorf("staging file leaked: %v", leftovers)
		}
	})

	t.Run("refuses symlink destination", func(t *testing.T) {
		link := filepath.Join(dir, "link.jsonl")
		if err := os.Symlink(dst, link); err != nil {
			t.Skip("symlinks unavailable")
		}
		if _, err := copyConversationStripped(src, link); err == nil {
			t.Error("expected error for symlink destination")
		}
	})
}
