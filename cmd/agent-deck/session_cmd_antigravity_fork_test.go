package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/asheshgoplani/agent-deck/internal/git"
	"github.com/asheshgoplani/agent-deck/internal/session"
)

func TestSessionFork_AntigravityReachesNativeForkPath(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	session.ClearUserConfigCache()
	t.Cleanup(session.ClearUserConfigCache)

	convID := "22222222-3333-4444-5555-666666666666"
	convDir := filepath.Join(home, ".gemini", "antigravity-cli", "conversations")
	if err := os.MkdirAll(convDir, 0o755); err != nil {
		t.Fatalf("mkdir conversations dir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(convDir, convID+".db"), []byte("sqlite"), 0o600); err != nil {
		t.Fatalf("write conversation db: %v", err)
	}

	parent := session.NewInstanceWithGroupAndTool("agy-parent", home, "agy-group", "antigravity")
	parent.Command = "agy"
	parent.AntigravitySessionID = convID
	parent.AntigravityDetectedAt = time.Now()

	profile := "agy_fork_hook"
	storage, err := session.NewStorageWithProfile(profile)
	if err != nil {
		t.Fatalf("NewStorageWithProfile: %v", err)
	}
	t.Cleanup(func() { _ = storage.Close() })
	if err := storage.SaveWithGroups([]*session.Instance{parent}, session.NewGroupTreeWithGroups([]*session.Instance{parent}, nil)); err != nil {
		t.Fatalf("SaveWithGroups: %v", err)
	}

	var capturedFork *session.Instance
	oldHook := sessionForkBeforeStartHook
	sessionForkBeforeStartHook = func(_ *session.Instance, forked *session.Instance, _ git.WorktreeStateOptions) {
		capturedFork = forked
	}
	t.Cleanup(func() { sessionForkBeforeStartHook = oldHook })

	handleSessionFork(profile, []string{"agy-parent", "-t", "agy-child"})

	if capturedFork == nil || capturedFork.Tool != "antigravity" {
		t.Fatalf("CLI fork did not reach Antigravity dispatcher: %+v", capturedFork)
	}
	if !capturedFork.IsForkAwaitingStart {
		t.Fatalf("captured Antigravity fork should have IsForkAwaitingStart=true")
	}
	for _, want := range []string{"agy", "--conversation " + convID, "--prompt-interactive /fork"} {
		if !strings.Contains(capturedFork.ForkStartCommand, want) {
			t.Fatalf("captured Antigravity fork command = %q, want to contain %q", capturedFork.ForkStartCommand, want)
		}
	}
}
