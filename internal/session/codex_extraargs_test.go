package session

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestBuildCodexCommand_AppendsExtraArgsToFreshAndResume(t *testing.T) {
	tmpDir := t.TempDir()
	originalHome := os.Getenv("HOME")
	os.Setenv("HOME", tmpDir)
	defer os.Setenv("HOME", originalHome)
	t.Setenv("CODEX_HOME", filepath.Join(tmpDir, ".codex"))
	ClearUserConfigCache()
	defer ClearUserConfigCache()

	inst := NewInstanceWithTool("codex-extra", "/tmp/codex-extra", "codex")
	inst.ExtraArgs = []string{"--sandbox", "read-only", "value with spaces"}

	fresh := inst.buildCodexCommand("codex")
	wantFlags := "--sandbox read-only 'value with spaces'"
	if !strings.Contains(fresh, wantFlags) {
		t.Fatalf("fresh Codex command missing shell-quoted extra args %q:\n%s", wantFlags, fresh)
	}

	inst.CodexSessionID = "019d1af6-c425-7791-8fd1-38c0fc43062c"
	writeFakeCodexRollout(t, filepath.Join(tmpDir, ".codex"), inst.CodexSessionID)
	resume := inst.buildCodexCommand("codex")
	wantResume := wantFlags + " resume " + inst.CodexSessionID
	if !strings.Contains(resume, wantResume) {
		t.Fatalf("Codex extra args must precede the resume subcommand; want %q in:\n%s", wantResume, resume)
	}
}

func TestCreateForkedCodexInstance_PreservesExtraArgs(t *testing.T) {
	codexHome := t.TempDir()
	t.Setenv("CODEX_HOME", codexHome)
	parent := NewInstanceWithTool("codex parent", "/tmp/codex-parent", "codex")
	parent.CodexSessionID = "11111111-2222-3333-4444-555555555555"
	seedCodexRollout(t, codexHome, parent.CodexSessionID)
	parent.ExtraArgs = []string{"--sandbox", "read-only"}

	forked, cmd, err := parent.CreateForkedCodexInstanceWithOptions("codex fork", "", nil)
	if err != nil {
		t.Fatalf("CreateForkedCodexInstanceWithOptions: %v", err)
	}
	if got := strings.Join(forked.ExtraArgs, " "); got != "--sandbox read-only" {
		t.Fatalf("fork ExtraArgs = %q, want %q", got, "--sandbox read-only")
	}
	parent.ExtraArgs[0] = "--mutated"
	if forked.ExtraArgs[0] != "--sandbox" {
		t.Fatal("fork ExtraArgs aliases the parent slice")
	}
	want := "--sandbox read-only fork " + parent.CodexSessionID
	if !strings.Contains(cmd, want) {
		t.Fatalf("Codex extra args must precede the fork subcommand; want %q in:\n%s", want, cmd)
	}
}

func TestBuildCodexCommand_CustomCommandDoesNotAppendExtraArgs(t *testing.T) {
	inst := NewInstanceWithTool("custom Codex", "/tmp/custom-codex", "codex")
	inst.ExtraArgs = []string{"--sandbox", "read-only"}

	cmd := inst.buildCodexCommand("codex-wrapper --already-configured")
	if strings.Contains(cmd, "--sandbox") || strings.Contains(cmd, "read-only") {
		t.Fatalf("custom-command passthrough must not append persisted extra args:\n%s", cmd)
	}
	if !strings.Contains(cmd, "codex-wrapper --already-configured") {
		t.Fatalf("custom command changed unexpectedly:\n%s", cmd)
	}
}

func TestSupportsExtraArgs_ClaudeAndCodexOnly(t *testing.T) {
	for _, tool := range []string{"claude", "codex"} {
		if !SupportsExtraArgs(tool) {
			t.Errorf("SupportsExtraArgs(%q) = false, want true", tool)
		}
	}
	for _, tool := range []string{"shell", "gemini", "opencode"} {
		if SupportsExtraArgs(tool) {
			t.Errorf("SupportsExtraArgs(%q) = true, want false", tool)
		}
	}
}
