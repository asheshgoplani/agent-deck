package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/asheshgoplani/agent-deck/internal/session"
)

// Issues #2396 and #2400 (reproduced live with codex-cli 0.147): `launch`
// never persisted the Codex identity (PostStartSync skips Codex and the async
// detection dies with the launch process), so the registry row had no
// codex_session_id until a follow-up send hydrated it. `session show` derived
// the identity from the live pane, which hid the gap: first-turn output fell
// back to unbound pane text (#2396), and archiving killed the only evidence,
// losing codex_session_id and transcript_path for good (#2400).

const codexFirstTurnThread = "01a0e047-dedd-7981-805a-ef829ec6f478"

// startCodexFirstTurnPane starts a pane whose fake Codex holds the thread's
// writer lock and rollout open, like a real Codex after its launch turn, and
// persists the instance the way launch left it: without a Codex identity.
func startCodexFirstTurnPane(t *testing.T, profile string) *session.Instance {
	t.Helper()
	root := t.TempDir()
	home := filepath.Join(root, "codex")
	t.Setenv("CODEX_HOME", home)
	project := filepath.Join(root, "project")
	rollout := filepath.Join(home, "sessions", "2026", "09", "27", "rollout-2026-09-27T00-33-21-"+codexFirstTurnThread+".jsonl")
	for _, dir := range []string{project, filepath.Join(home, "thread-writer-locks"), filepath.Dir(rollout), filepath.Join(root, "bin")} {
		if err := os.MkdirAll(dir, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	records := `{"timestamp":"2026-09-27T00:33:21Z","type":"session_meta","payload":{"session_id":"` + codexFirstTurnThread + `","id":"` + codexFirstTurnThread + `","cwd":"` + project + `","thread_source":"user"}}` + "\n" +
		`{"timestamp":"2026-09-27T00:33:22Z","type":"event_msg","payload":{"type":"task_started","turn_id":"turn-launch"}}` + "\n" +
		`{"timestamp":"2026-09-27T00:33:30Z","type":"event_msg","payload":{"type":"task_complete","turn_id":"turn-launch","last_agent_message":"FIRST-TURN"}}` + "\n"
	if err := os.WriteFile(rollout, []byte(records), 0o600); err != nil {
		t.Fatal(err)
	}
	lock := filepath.Join(home, "thread-writer-locks", codexFirstTurnThread+".lock")
	fakeCodex := filepath.Join(root, "bin", "codex")
	if err := os.WriteFile(fakeCodex, []byte("#!/bin/sh\nexec 9>>\"$1\" 8<\"$2\"\nwhile :; do sleep 1; done\n"), 0o700); err != nil {
		t.Fatal(err)
	}

	inst := session.NewInstanceWithTool("codex-first-turn", project, "codex")
	inst.Status = session.StatusWaiting
	tmuxSess := inst.GetTmuxSession()
	if tmuxSess == nil {
		t.Fatal("missing tmux session")
	}
	if err := tmuxSess.Start(fakeCodex + " " + lock + " " + rollout); err != nil {
		t.Fatalf("start fake codex pane: %v", err)
	}
	t.Cleanup(func() { _ = tmuxSess.Kill() })
	deadline := time.Now().Add(10 * time.Second)
	for inst.LiveCodexThreadID() != codexFirstTurnThread {
		if time.Now().After(deadline) {
			t.Fatalf("pane process never held the thread open (live thread %q)", inst.LiveCodexThreadID())
		}
		time.Sleep(100 * time.Millisecond)
	}

	storage, err := session.NewStorageWithProfile(profile)
	if err != nil {
		t.Fatal(err)
	}
	defer storage.Close()
	if err := storage.SaveWithGroups([]*session.Instance{inst}, nil); err != nil {
		t.Fatal(err)
	}
	if got := persistedCodexIdentity(t, storage, inst.ID); got != "" {
		t.Fatalf("precondition: persisted identity = %q, want none", got)
	}
	return inst
}

func reloadedCodexIdentity(t *testing.T, profile, id string) string {
	t.Helper()
	storage, err := session.NewStorageWithProfile(profile)
	if err != nil {
		t.Fatal(err)
	}
	defer storage.Close()
	return persistedCodexIdentity(t, storage, id)
}

func TestIssue2400_ArchiveKeepsLiveCodexIdentity(t *testing.T) {
	const profile = "codex_archive_2400"
	inst := startCodexFirstTurnPane(t, profile)

	captureStdout(t, func() { handleSessionArchive(profile, []string{inst.ID, "--json"}) })

	if inst.GetTmuxSession().Exists() {
		t.Fatal("archive did not stop the session")
	}
	if got := reloadedCodexIdentity(t, profile, inst.ID); got != codexFirstTurnThread {
		t.Fatalf("persisted identity after archive = %q, want %q", got, codexFirstTurnThread)
	}
}

func TestIssue2396_FirstTurnOutputIsBoundToItsConversation(t *testing.T) {
	const profile = "codex_first_turn_2396"
	inst := startCodexFirstTurnPane(t, profile)

	raw := captureStdout(t, func() { handleSessionOutput(profile, []string{inst.ID, "--json"}) })
	var got map[string]interface{}
	if err := json.Unmarshal([]byte(strings.TrimSpace(raw)), &got); err != nil {
		t.Fatalf("decode output %q: %v", raw, err)
	}
	if got["content"] != "FIRST-TURN" || got["conversation_id"] != codexFirstTurnThread ||
		got["codex_turn_generation"] != codexFirstTurnThread+":turn-launch" || got["timestamp"] == "" {
		t.Fatalf("first-turn output is not bound to its conversation: %v", got)
	}
	if persisted := reloadedCodexIdentity(t, profile, inst.ID); persisted != codexFirstTurnThread {
		t.Fatalf("persisted identity after output = %q, want %q", persisted, codexFirstTurnThread)
	}
}
