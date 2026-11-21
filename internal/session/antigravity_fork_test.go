package session

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/asheshgoplani/agent-deck/internal/statedb"
)

func TestSupportsNativeFork_Antigravity(t *testing.T) {
	if !SupportsNativeFork("antigravity") {
		t.Fatal("SupportsNativeFork(antigravity) = false, want true")
	}
}

func TestCanForkAntigravity(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	ClearUserConfigCache()
	t.Cleanup(ClearUserConfigCache)

	convID := "11111111-2222-3333-4444-555555555555"
	convDir := filepath.Join(home, ".gemini", "antigravity-cli", "conversations")
	if err := os.MkdirAll(convDir, 0o755); err != nil {
		t.Fatalf("mkdir conversations dir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(convDir, convID+".db"), []byte("sqlite"), 0o600); err != nil {
		t.Fatalf("write conversation db: %v", err)
	}

	t.Run("unbound cannot fork", func(t *testing.T) {
		inst := NewInstanceWithGroupAndTool("agy", home, "default", "antigravity")
		inst.Command = "agy"
		if inst.CanFork() {
			t.Fatal("CanFork() = true for unbound Antigravity session, want false")
		}
	})

	t.Run("recently detected bound ID can fork", func(t *testing.T) {
		inst := NewInstanceWithGroupAndTool("agy", home, "default", "antigravity")
		inst.Command = "agy"
		inst.AntigravitySessionID = "recent-conv-1"
		inst.AntigravityDetectedAt = time.Now()
		if !inst.CanFork() {
			t.Fatal("CanFork() = false for recently bound Antigravity session, want true")
		}
	})

	t.Run("stale bound ID with on-disk db can fork", func(t *testing.T) {
		inst := NewInstanceWithGroupAndTool("agy", home, "default", "antigravity")
		inst.Command = "agy"
		inst.AntigravitySessionID = convID
		inst.AntigravityDetectedAt = time.Now().Add(-2 * time.Hour)
		if !inst.CanFork() {
			t.Fatal("CanFork() = false for stale Antigravity ID with on-disk db, want true")
		}
	})

	t.Run("stale bound ID missing on disk cannot fork", func(t *testing.T) {
		inst := NewInstanceWithGroupAndTool("agy", home, "default", "antigravity")
		inst.Command = "agy"
		inst.AntigravitySessionID = "missing-conv-id"
		inst.AntigravityDetectedAt = time.Now().Add(-2 * time.Hour)
		if inst.CanFork() {
			t.Fatal("CanFork() = true for stale Antigravity ID missing from disk, want false")
		}
	})

	t.Run("explicit --conversation in command or wrapper can fork", func(t *testing.T) {
		instCmd := NewInstanceWithGroupAndTool("agy-cmd", home, "default", "antigravity")
		instCmd.Command = "agy --conversation explicit-123"
		if !instCmd.CanFork() {
			t.Fatal("CanFork() = false for explicit --conversation in Command, want true")
		}

		instWrap := NewInstanceWithGroupAndTool("agy-wrap", home, "default", "antigravity")
		instWrap.Command = "agy"
		instWrap.Wrapper = "{command} --conversation explicit-456"
		if !instWrap.CanFork() {
			t.Fatal("CanFork() = false for explicit --conversation in Wrapper, want true")
		}
	})

	t.Run("unsafe session ID is rejected", func(t *testing.T) {
		inst := NewInstanceWithGroupAndTool("agy-bad", home, "default", "antigravity")
		inst.Command = "agy"
		inst.AntigravitySessionID = "abc; rm -rf /"
		inst.AntigravityDetectedAt = time.Now()
		if inst.CanFork() {
			t.Fatal("CanFork() = true for unsafe Antigravity session ID, want false")
		}
	})

	t.Run("SSH session with bound ID skips local disk check", func(t *testing.T) {
		inst := NewInstanceWithGroupAndTool("agy-ssh", "/remote/repo", "default", "antigravity")
		inst.Command = "agy"
		inst.SSHHost = "devbox"
		inst.AntigravitySessionID = "remote-conv-9"
		inst.AntigravityDetectedAt = time.Now().Add(-2 * time.Hour)
		if !inst.CanFork() {
			t.Fatal("CanFork() = false for remote Antigravity session with bound ID, want true")
		}
	})
}

func TestCreateForkedAntigravityInstance_TmuxFork(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	restore := resetUserConfigCache(t, &UserConfig{})
	defer restore()

	repoDir := filepath.Join(home, "repo")
	wtDir := filepath.Join(home, "repo-wt")

	parent := NewInstanceWithGroupAndTool("agy-parent", repoDir, "projects/agy", "antigravity")
	parent.Command = "antigravity --mode plan"
	parent.Wrapper = "{command} --conversation conv-parent-1"

	opts := &ClaudeOptions{
		WorkDir:          wtDir,
		WorktreePath:     wtDir,
		WorktreeRepoRoot: repoDir,
		WorktreeBranch:   "feat/agy-fork",
	}

	forked, cmd, err := parent.CreateForkedInstanceForTool("agy-child", "", opts)
	if err != nil {
		t.Fatalf("CreateForkedInstanceForTool: %v", err)
	}
	if forked.Tool != "antigravity" {
		t.Fatalf("forked.Tool = %q, want antigravity", forked.Tool)
	}
	if forked.GroupPath != "projects/agy" {
		t.Fatalf("forked.GroupPath = %q, want inherited group", forked.GroupPath)
	}
	if forked.ProjectPath != wtDir || forked.WorktreeBranch != "feat/agy-fork" {
		t.Fatalf("forked worktree fields not preserved: path=%q branch=%q", forked.ProjectPath, forked.WorktreeBranch)
	}
	if forked.Wrapper != "" {
		t.Fatalf("auto-wrapped parent --conversation should be stripped from forked.Wrapper, got %q", forked.Wrapper)
	}
	if forked.Command != "agy --mode plan" {
		t.Fatalf("forked.Command = %q, want clean base command without parent conversation", forked.Command)
	}
	if !forked.IsForkAwaitingStart {
		t.Fatal("fork start should be deferred")
	}
	if forked.antigravityForkParentID != "conv-parent-1" {
		t.Fatalf("antigravityForkParentID = %q, want conv-parent-1", forked.antigravityForkParentID)
	}
	for _, want := range []string{
		"agy --mode plan",
		"--conversation conv-parent-1",
		"--add-dir " + wtDir,
	} {
		if !strings.Contains(cmd, want) {
			t.Errorf("Antigravity fork command = %q, want to contain %q", cmd, want)
		}
	}
	if strings.Contains(cmd, "/fork") || strings.Contains(cmd, "--prompt-interactive") {
		t.Fatalf("fork must not be embedded in launch command: %q", cmd)
	}

	// Once the child binds its own conversation ID, future restarts resume the child ID, never /fork.
	forked.AntigravitySessionID = "conv-child-2"
	resumeCmd := forked.buildAntigravityResumeCommand()
	if !strings.Contains(resumeCmd, "--conversation conv-child-2") || strings.Contains(resumeCmd, "/fork") {
		t.Fatalf("buildAntigravityResumeCommand() = %q, want child conversation resume without /fork", resumeCmd)
	}
}

func TestCreateForkedAntigravityInstance_WrappedTmuxFork(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	restore := resetUserConfigCache(t, &UserConfig{})
	defer restore()

	parent := NewInstanceWithGroupAndTool("agy-wrapped", home, "default", "antigravity")
	parent.Command = "agy"
	parent.Wrapper = "env FOO=1 {command}"
	parent.AntigravitySessionID = "conv-wrap-1"
	parent.AntigravityDetectedAt = time.Now()

	forked, cmd, err := parent.CreateForkedInstanceForTool("agy-wrapped-fork", "", nil)
	if err != nil {
		t.Fatalf("CreateForkedInstanceForTool: %v", err)
	}
	if forked.Wrapper != "env FOO=1 {command}" {
		t.Fatalf("custom wrapper should be preserved, got %q", forked.Wrapper)
	}
	if strings.Contains(cmd, "--prompt-interactive") {
		t.Fatalf("wrapper fallback command must not embed --prompt-interactive: %q", cmd)
	}
	if !strings.Contains(cmd, "--conversation conv-wrap-1") {
		t.Fatalf("wrapper fallback command must still pass --conversation: %q", cmd)
	}
}

func TestAntigravityConversationDiscovery(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	workDir := filepath.Join(home, "project")
	otherDir := filepath.Join(home, "other")
	if err := os.MkdirAll(workDir, 0o755); err != nil {
		t.Fatalf("mkdir workDir: %v", err)
	}
	cacheDir := filepath.Join(home, ".gemini", "antigravity-cli", "cache")
	if err := os.MkdirAll(cacheDir, 0o755); err != nil {
		t.Fatalf("mkdir cacheDir: %v", err)
	}

	now := time.Now().UTC()
	meta := map[string]any{
		"conversations": map[string]any{
			"parent-conv": map[string]any{
				"summary": map[string]any{
					"ID":            "parent-conv",
					"UpdatedAt":     now.Add(-2 * time.Minute).Format(time.RFC3339Nano),
					"WorkspaceURIs": []string{"file://" + workDir},
					"Internal":      false,
				},
				"is_internal": false,
			},
			"internal-subagent": map[string]any{
				"summary": map[string]any{
					"ID":            "internal-subagent",
					"UpdatedAt":     now.Format(time.RFC3339Nano),
					"WorkspaceURIs": []string{"file://" + workDir},
					"Internal":      true,
				},
				"is_internal": true,
			},
			"other-workspace": map[string]any{
				"summary": map[string]any{
					"ID":            "other-workspace",
					"UpdatedAt":     now.Format(time.RFC3339Nano),
					"WorkspaceURIs": []string{"file://" + otherDir},
					"Internal":      false,
				},
				"is_internal": false,
			},
			"child-conv": map[string]any{
				"summary": map[string]any{
					"ID":            "child-conv",
					"UpdatedAt":     now.Add(-10 * time.Second).Format(time.RFC3339Nano),
					"WorkspaceURIs": []string{"file://" + workDir},
					"Internal":      false,
				},
				"is_internal": false,
			},
		},
	}
	raw, err := json.Marshal(meta)
	if err != nil {
		t.Fatalf("marshal metadata: %v", err)
	}
	if err := os.WriteFile(filepath.Join(cacheDir, "conversation_metadata.json"), raw, 0o600); err != nil {
		t.Fatalf("write metadata: %v", err)
	}

	dataDir := filepath.Join(home, ".gemini", "antigravity-cli")

	// Excludes internal subagent and other workspace; picks newest non-internal match.
	if got := discoverAntigravityConversationFromDisk(dataDir, workDir, workDir, "", "", 0); got != "child-conv" {
		t.Fatalf("discover = %q, want child-conv", got)
	}

	// Already bound to parent-conv: keeps parent-conv instead of stealing sibling child-conv.
	if got := discoverAntigravityConversationFromDisk(dataDir, workDir, workDir, "parent-conv", "", 0); got != "parent-conv" {
		t.Fatalf("discover with existing binding = %q, want parent-conv", got)
	}

	// Fork excluding parent-conv with startedAt after parent-conv: picks child-conv, never parent-conv.
	startedAt := now.Add(-30 * time.Second).UnixMilli()
	if got := discoverAntigravityConversationFromDisk(dataDir, workDir, workDir, "", "parent-conv", startedAt); got != "child-conv" {
		t.Fatalf("discover for fork = %q, want child-conv", got)
	}

	// Before child-conv exists (exclude child-conv too): returns empty instead of adopting parent-conv.
	if got := discoverAntigravityConversationFromDisk(dataDir, workDir, workDir, "", "child-conv", startedAt); got != "" {
		t.Fatalf("discover before child exists = %q, want empty (parent predates startedAt)", got)
	}
}

func TestAntigravitySessionIDPersistenceAndSetField(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	now := time.Now().UTC().Truncate(time.Second)
	td := WriteAntigravitySessionIDToToolData(json.RawMessage(`{"other":1}`), "conv-a", now, false)
	if got := ReadAntigravitySessionIDFromToolData(td); got != "conv-a" {
		t.Fatalf("ReadAntigravitySessionIDFromToolData = %q, want conv-a", got)
	}
	if got := ReadAntigravityDetectedAtFromToolData(td); !got.Equal(now) {
		t.Fatalf("ReadAntigravityDetectedAtFromToolData = %v, want %v", got, now)
	}

	// Omitting on an unaware save preserves the sticky binding.
	unaware := WriteAntigravitySessionIDToToolData(json.RawMessage(`{"other":2}`), "", time.Time{}, false)
	merged := statedb.MergeToolDataExtras(td, unaware)
	if got := ReadAntigravitySessionIDFromToolData(merged); got != "conv-a" {
		t.Fatalf("MergeToolDataExtras dropped sticky antigravity_session_id: got %q", got)
	}

	// Explicit clear clears the sticky binding.
	cleared := WriteAntigravitySessionIDToToolData(json.RawMessage(`{"other":2}`), "", time.Time{}, true)
	mergedCleared := statedb.MergeToolDataExtras(td, cleared)
	if got := ReadAntigravitySessionIDFromToolData(mergedCleared); got != "" {
		t.Fatalf("explicit clear did not clear antigravity_session_id: got %q", got)
	}

	// SetField validates, sets, and clears antigravity-session-id.
	inst := NewInstanceWithGroupAndTool("agy-set", home, "default", "antigravity")
	if _, _, err := SetField(inst, FieldAntigravitySessionID, "conv-123", nil); err != nil {
		t.Fatalf("SetField valid antigravity-session-id: %v", err)
	}
	if inst.AntigravitySessionID != "conv-123" || inst.antigravitySessionCleared {
		t.Fatalf("SetField did not bind id=%q cleared=%v", inst.AntigravitySessionID, inst.antigravitySessionCleared)
	}
	if _, _, err := SetField(inst, FieldAntigravitySessionID, "bad id; evil", nil); err == nil {
		t.Fatal("SetField accepted unsafe antigravity-session-id")
	}
	if _, _, err := SetField(inst, FieldAntigravitySessionID, "  ", nil); err != nil {
		t.Fatalf("SetField clear antigravity-session-id: %v", err)
	}
	if inst.AntigravitySessionID != "" || !inst.antigravitySessionCleared {
		t.Fatalf("SetField clear left id=%q cleared=%v", inst.AntigravitySessionID, inst.antigravitySessionCleared)
	}
}

func TestAntigravityFork_SendsTmuxCommandAfterComposerReady(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	ClearUserConfigCache()
	t.Cleanup(ClearUserConfigCache)

	binDir := filepath.Join(home, "bin")
	if err := os.MkdirAll(binDir, 0o755); err != nil {
		t.Fatalf("mkdir binDir: %v", err)
	}
	argsLog := filepath.Join(home, "agy-args.log")
	stdinLog := filepath.Join(home, "agy-stdin.log")
	stubPath := filepath.Join(binDir, "agy")
	stubScript := `#!/bin/bash
printf '%s\n' "$*" > ` + argsLog + `
printf 'Trust this folder?\n> Yes\n  No\n'
if IFS= read -r -t 0.8 line; then
  printf 'premature: %s\n' "$line" >> ` + stdinLog + `
fi
printf '\033[H\033[2J'
printf '────────────────\n>\n────────────────\n? for shortcuts\n'
while IFS= read -r line; do
  printf '%s\n' "$line" >> ` + stdinLog + `
done
`
	if err := os.WriteFile(stubPath, []byte(stubScript), 0o755); err != nil {
		t.Fatalf("write stub agy: %v", err)
	}
	t.Setenv("PATH", binDir+string(os.PathListSeparator)+os.Getenv("PATH"))

	t.Run("bare agy", func(t *testing.T) {
		_ = os.Remove(argsLog)
		_ = os.Remove(stdinLog)
		parent := NewInstanceWithGroupAndTool("agy-e2e-parent", home, "default", "antigravity")
		parent.Command = "agy"
		parent.AntigravitySessionID = "parent-conv-e2e"
		parent.AntigravityDetectedAt = time.Now()

		forked, _, err := parent.CreateForkedInstanceForTool("agy-e2e-child", "", nil)
		if err != nil {
			t.Fatalf("CreateForkedInstanceForTool: %v", err)
		}
		t.Cleanup(func() { _ = forked.Kill() })

		if err := forked.Start(); err != nil {
			t.Fatalf("forked.Start: %v", err)
		}

		deadline := time.Now().Add(5 * time.Second)
		var gotArgs, gotStdin string
		for time.Now().Before(deadline) {
			if raw, err := os.ReadFile(argsLog); err == nil {
				gotArgs = strings.TrimSpace(string(raw))
			}
			if raw, err := os.ReadFile(stdinLog); err == nil {
				gotStdin = strings.TrimSpace(string(raw))
			}
			if gotArgs != "" && gotStdin != "" {
				break
			}
			time.Sleep(50 * time.Millisecond)
		}
		if gotArgs != "--conversation parent-conv-e2e" {
			t.Fatalf("stub agy args = %q, want only the parent conversation", gotArgs)
		}
		if gotStdin != "/fork" {
			t.Fatalf("bare agy stdin = %q, want only /fork after the composer appears", gotStdin)
		}

		// Simulate Antigravity writing both parent and newly forked child conversations to cache.
		cacheDir := filepath.Join(home, ".gemini", "antigravity-cli", "cache")
		if err := os.MkdirAll(cacheDir, 0o755); err != nil {
			t.Fatalf("mkdir cacheDir: %v", err)
		}
		meta := map[string]any{
			"conversations": map[string]any{
				"parent-conv-e2e": map[string]any{
					"summary": map[string]any{
						"ID":            "parent-conv-e2e",
						"UpdatedAt":     time.Now().UTC().Format(time.RFC3339Nano),
						"WorkspaceURIs": []string{"file://" + home},
					},
				},
				"child-conv-e2e": map[string]any{
					"summary": map[string]any{
						"ID":            "child-conv-e2e",
						"UpdatedAt":     time.Now().UTC().Format(time.RFC3339Nano),
						"WorkspaceURIs": []string{"file://" + home},
					},
				},
			},
		}
		rawMeta, _ := json.Marshal(meta)
		if err := os.WriteFile(filepath.Join(cacheDir, "conversation_metadata.json"), rawMeta, 0o600); err != nil {
			t.Fatalf("write metadata: %v", err)
		}
		if got := forked.SyncAntigravitySessionID(); got != "child-conv-e2e" {
			t.Fatalf("SyncAntigravitySessionID() = %q, want child-conv-e2e (never parent)", got)
		}
	})

	t.Run("custom wrapper with initial message", func(t *testing.T) {
		_ = os.Remove(argsLog)
		_ = os.Remove(stdinLog)
		parent := NewInstanceWithGroupAndTool("agy-wrap-parent", home, "default", "antigravity")
		parent.Command = "agy"
		parent.Wrapper = "env AGY_WRAP=1 {command}"
		parent.AntigravitySessionID = "parent-wrap-e2e"
		parent.AntigravityDetectedAt = time.Now()

		forked, _, err := parent.CreateForkedInstanceForTool("agy-wrap-child", "", nil)
		if err != nil {
			t.Fatalf("CreateForkedInstanceForTool: %v", err)
		}
		t.Cleanup(func() { _ = forked.Kill() })

		if err := forked.StartWithMessage("hello child"); err != nil {
			t.Fatalf("forked.StartWithMessage: %v", err)
		}

		deadline := time.Now().Add(5 * time.Second)
		var gotArgs, gotStdin string
		for time.Now().Before(deadline) {
			if raw, err := os.ReadFile(argsLog); err == nil {
				gotArgs = strings.TrimSpace(string(raw))
			}
			if raw, err := os.ReadFile(stdinLog); err == nil {
				gotStdin = strings.TrimSpace(string(raw))
			}
			if gotArgs != "" && strings.Contains(gotStdin, "hello child") {
				break
			}
			time.Sleep(50 * time.Millisecond)
		}
		if !strings.Contains(gotArgs, "--conversation parent-wrap-e2e") || strings.Contains(gotArgs, "--prompt-interactive") {
			t.Fatalf("wrapped stub agy args = %q, want --conversation without --prompt-interactive", gotArgs)
		}
		if gotStdin != "/fork\nhello child" {
			t.Fatalf("wrapped fork stdin = %q, want /fork followed by the initial message", gotStdin)
		}
	})
}
