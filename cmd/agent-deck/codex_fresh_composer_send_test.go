package main

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/asheshgoplani/agent-deck/internal/session"
)

// A fresh Codex composer (0.155+) owns its thread before any rollout exists:
// the only trace is the thread writer lock the live process holds open. The
// send guard must accept that thread with an empty prior generation, and the
// first rollout turn must then produce the exact accepted-turn receipt.
func TestCodexAcceptanceGuardAcceptsFreshComposerThread(t *testing.T) {
	root := t.TempDir()
	home := filepath.Join(root, "codex")
	t.Setenv("CODEX_HOME", home)
	project := filepath.Join(root, "project")
	for _, dir := range []string{project, filepath.Join(home, "thread-writer-locks"), filepath.Join(root, "bin")} {
		if err := os.MkdirAll(dir, 0o700); err != nil {
			t.Fatal(err)
		}
	}

	const thread = "01a0cc56-3416-7531-9a25-c53c0d193753"
	lock := filepath.Join(home, "thread-writer-locks", thread+".lock")
	fakeCodex := filepath.Join(root, "bin", "codex")
	script := "#!/bin/sh\nexec 9>>\"$1\"\nwhile :; do sleep 1; done\n"
	if err := os.WriteFile(fakeCodex, []byte(script), 0o700); err != nil {
		t.Fatal(err)
	}

	inst := session.NewInstanceWithTool("fresh-codex-composer", project, "codex")
	inst.Status = session.StatusWaiting
	tmuxSess := inst.GetTmuxSession()
	if tmuxSess == nil {
		t.Fatal("missing tmux session")
	}
	if err := tmuxSess.Start(fakeCodex + " " + lock); err != nil {
		t.Fatalf("start fake codex pane: %v", err)
	}
	t.Cleanup(func() { _ = tmuxSess.Kill() })
	deadline := time.Now().Add(10 * time.Second)
	for inst.LiveCodexThreadID() != thread {
		if time.Now().After(deadline) {
			t.Fatalf("pane process never held the writer lock (live thread %q)", inst.LiveCodexThreadID())
		}
		time.Sleep(100 * time.Millisecond)
	}

	storage, err := session.NewStorageWithProfile("fresh_codex_composer")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = storage.Close() })
	if err := storage.SaveWithGroups([]*session.Instance{inst}, nil); err != nil {
		t.Fatal(err)
	}

	if err := hydrateLegacyCodexIdentity(inst, []*session.Instance{inst}, storage); err != nil {
		t.Fatalf("fresh composer identity: %v", err)
	}
	guard, err := acquireCodexAcceptanceGuard(inst, time.Second)
	if err != nil {
		t.Fatalf("fresh composer guard: %v", err)
	}
	defer guard.Release()
	if inst.CodexSessionID != thread || !guard.fence.available || guard.fence.priorTurnGeneration != "" {
		t.Fatalf("unexpected fresh guard: id=%q fence=%#v", inst.CodexSessionID, guard.fence)
	}
	if persisted := persistedCodexIdentity(t, storage, inst.ID); persisted != thread {
		t.Fatalf("persisted identity = %q, want %q", persisted, thread)
	}
	if err := validateCodexAcceptanceFence(inst, guard.fence); err != nil {
		t.Fatalf("fence before the first turn: %v", err)
	}

	// Codex writes the rollout when the submitted turn starts.
	writeLegacyCodexRollout(t, home, thread, "23")
	receipt := waitForAcceptedCodexTurn(inst, deliverySubmitted, time.Now(), guard.fence)
	if receipt == nil || receipt.TurnGeneration != thread+":turn-existing" {
		t.Fatalf("first turn receipt = %#v", receipt)
	}
}
