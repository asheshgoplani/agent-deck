package main

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/asheshgoplani/agent-deck/internal/session"
)

// Issue #2394: panes launched by earlier builds can carry a CODEX_SESSION_ID
// that a bootstrap disk scan guessed from a sibling's rollout. The first send
// persisted that pane value as the identity, so the send fenced the sibling's
// rollout (no accepted generation) and output read the sibling's reply. The
// thread the pane's live Codex process holds open outranks the pane value.
func TestIssue2394_HydratePrefersLiveThreadOverGuessedPaneIdentity(t *testing.T) {
	root := t.TempDir()
	home := filepath.Join(root, "codex")
	t.Setenv("CODEX_HOME", home)
	project := filepath.Join(root, "project")
	for _, dir := range []string{project, filepath.Join(home, "thread-writer-locks"), filepath.Join(root, "bin")} {
		if err := os.MkdirAll(dir, 0o700); err != nil {
			t.Fatal(err)
		}
	}

	const own = "01a0e049-2af2-76a0-87d6-0289d8e10025"
	const sibling = "01a0e048-bf61-7921-bd32-d24c8baa7f5a"
	writeLegacyCodexRollout(t, home, sibling, "27")

	lock := filepath.Join(home, "thread-writer-locks", own+".lock")
	fakeCodex := filepath.Join(root, "bin", "codex")
	if err := os.WriteFile(fakeCodex, []byte("#!/bin/sh\nexec 9>>\"$1\"\nwhile :; do sleep 1; done\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	inst := session.NewInstanceWithTool("codex-no-launch-message", project, "codex")
	inst.Status = session.StatusWaiting
	tmuxSess := inst.GetTmuxSession()
	if tmuxSess == nil {
		t.Fatal("missing tmux session")
	}
	if err := tmuxSess.Start(fakeCodex + " " + lock); err != nil {
		t.Fatalf("start fake codex pane: %v", err)
	}
	t.Cleanup(func() { _ = tmuxSess.Kill() })
	if err := tmuxSess.SetEnvironment("CODEX_SESSION_ID", sibling); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(10 * time.Second)
	for inst.LiveCodexThreadID() != own {
		if time.Now().After(deadline) {
			t.Fatalf("pane process never held the writer lock (live thread %q)", inst.LiveCodexThreadID())
		}
		time.Sleep(100 * time.Millisecond)
	}

	storage, err := session.NewStorageWithProfile("codex_foreign_identity")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = storage.Close() })
	if err := storage.SaveWithGroups([]*session.Instance{inst}, nil); err != nil {
		t.Fatal(err)
	}

	if err := hydrateLegacyCodexIdentity(inst, []*session.Instance{inst}, storage); err != nil {
		t.Fatalf("hydrate: %v", err)
	}
	if inst.CodexSessionID != own {
		t.Fatalf("hydrated identity = %q, want the live process's thread %q", inst.CodexSessionID, own)
	}
	if persisted := persistedCodexIdentity(t, storage, inst.ID); persisted != own {
		t.Fatalf("persisted identity = %q, want %q", persisted, own)
	}
	guard, err := acquireCodexAcceptanceGuard(inst, time.Second)
	if err != nil {
		t.Fatalf("guard: %v", err)
	}
	defer guard.Release()
	if guard.fence.priorTurnGeneration != "" {
		t.Fatalf("fence = %#v, want the fresh thread's empty generation", guard.fence)
	}

	// The first turn of the child's own thread is the accepted generation.
	writeLegacyCodexRollout(t, home, own, "27")
	receipt := waitForAcceptedCodexTurn(inst, deliverySubmitted, time.Now(), guard.fence)
	if receipt == nil || receipt.TurnGeneration != own+":turn-existing" {
		t.Fatalf("first turn receipt = %#v", receipt)
	}
}
