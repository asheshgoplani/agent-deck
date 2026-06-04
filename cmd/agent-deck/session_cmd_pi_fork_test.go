package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/asheshgoplani/agent-deck/internal/git"
	"github.com/asheshgoplani/agent-deck/internal/session"
)

func TestSessionFork_PiUsesNativeForkBeforeStart(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	session.ClearUserConfigCache()
	t.Cleanup(session.ClearUserConfigCache)

	parent := session.NewInstanceWithGroupAndTool("pi-parent", home, "pi-group", "pi")
	parent.Command = "pi"
	parentSessionDir := filepath.Join(home, ".pi", "agent-deck", parent.ID)
	if err := os.MkdirAll(parentSessionDir, 0o755); err != nil {
		t.Fatalf("mkdir parent Pi session dir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(parentSessionDir, "session.jsonl"), []byte("{}\n"), 0o600); err != nil {
		t.Fatalf("write parent Pi JSONL: %v", err)
	}

	profile := "pi_fork_hook"
	storage, err := session.NewStorageWithProfile(profile)
	if err != nil {
		t.Fatalf("NewStorageWithProfile: %v", err)
	}
	t.Cleanup(func() { _ = storage.Close() })
	if err := storage.SaveWithGroups([]*session.Instance{parent}, session.NewGroupTreeWithGroups([]*session.Instance{parent}, nil)); err != nil {
		t.Fatalf("SaveWithGroups: %v", err)
	}

	var capturedParent *session.Instance
	var capturedFork *session.Instance
	oldHook := sessionForkBeforeStartHook
	sessionForkBeforeStartHook = func(parent *session.Instance, forked *session.Instance, _ git.WorktreeStateOptions) {
		capturedParent = parent
		capturedFork = forked
	}
	t.Cleanup(func() { sessionForkBeforeStartHook = oldHook })

	handleSessionFork(profile, []string{"pi-parent", "-t", "pi-child"})

	if capturedParent == nil || capturedParent.ID != parent.ID {
		t.Fatalf("hook captured parent = %+v, want parent %s", capturedParent, parent.ID)
	}
	if capturedFork == nil {
		t.Fatal("hook did not capture forked instance")
	}
	if capturedFork.Tool != "pi" {
		t.Fatalf("capturedFork.Tool = %q, want pi", capturedFork.Tool)
	}
	if capturedFork.Command != "pi" {
		t.Fatalf("capturedFork.Command = %q, want base pi command", capturedFork.Command)
	}
	if !capturedFork.IsForkAwaitingStart || capturedFork.ForkStartCommand == "" {
		t.Fatalf("captured Pi fork should carry first-start command, got awaiting=%v command=%q", capturedFork.IsForkAwaitingStart, capturedFork.ForkStartCommand)
	}
	for _, want := range []string{
		"parent_session_dir=${HOME}/.pi/agent-deck/" + parent.ID,
		"session_dir=${HOME}/.pi/agent-deck/" + capturedFork.ID,
		`pi --fork "$source_file" --session-dir "$session_dir"`,
	} {
		if !strings.Contains(capturedFork.ForkStartCommand, want) {
			t.Fatalf("Pi fork first-start command = %q, want to contain %q", capturedFork.ForkStartCommand, want)
		}
	}
	if strings.Contains(capturedFork.ForkStartCommand, "--continue") {
		t.Fatalf("Pi fork first-start command must not include --continue: %s", capturedFork.ForkStartCommand)
	}
}
