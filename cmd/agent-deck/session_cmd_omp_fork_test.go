package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/asheshgoplani/agent-deck/internal/git"
	"github.com/asheshgoplani/agent-deck/internal/session"
)

// Behavioral red-path guard: a real forkable OMP record must pass the CLI's
// tool gate and reach the shared native fork dispatcher.
func TestSessionFork_OMPReachesNativeForkPath(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	session.ClearUserConfigCache()
	t.Cleanup(session.ClearUserConfigCache)

	parent := session.NewInstanceWithGroupAndTool("omp-parent", home, "omp-group", "omp")
	parent.Command = "omp"
	parentSessionDir := filepath.Join(home, ".omp", "agent-deck", parent.ID)
	if err := os.MkdirAll(parentSessionDir, 0o755); err != nil {
		t.Fatalf("mkdir parent OMP session dir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(parentSessionDir, "session.jsonl"), []byte("{}\n"), 0o600); err != nil {
		t.Fatalf("write parent OMP JSONL: %v", err)
	}

	profile := "omp_fork_hook"
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

	handleSessionFork(profile, []string{"omp-parent", "-t", "omp-child"})

	if capturedFork == nil || capturedFork.Tool != "omp" {
		t.Fatalf("CLI fork did not reach OMP dispatcher: %+v", capturedFork)
	}
	if !capturedFork.IsForkAwaitingStart || !strings.Contains(capturedFork.ForkStartCommand, `omp --fork "$source_file"`) {
		t.Fatalf("captured OMP native fork command = %q", capturedFork.ForkStartCommand)
	}
}
