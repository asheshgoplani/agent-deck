package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/asheshgoplani/agent-deck/internal/send"
	"github.com/asheshgoplani/agent-deck/internal/session"
)

// Codex 0.155 runs an ephemeral thread-title generation thread beside the main
// thread. Its agent-turn-complete notify carries its own thread id and never
// has a rollout. Arriving while the main turn still runs, it must not replace
// the hook status or anchor, or `session send --defer-if-busy` reads it as the
// turn-finished edge and delivers into the running turn.
func TestDeferIfBusy_CodexTitleCompletionMidTurnDefers(t *testing.T) {
	profile := "defer_codex_title"
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))
	t.Setenv("XDG_DATA_HOME", filepath.Join(home, ".local", "share"))
	t.Setenv("XDG_CACHE_HOME", filepath.Join(home, ".cache"))
	codexHome := filepath.Join(home, ".codex")
	t.Setenv("CODEX_HOME", codexHome)
	t.Setenv("CODEX_SESSION_ID", "")
	session.ClearUserConfigCache()
	t.Cleanup(session.ClearUserConfigCache)

	const (
		mainSID   = "01a0cc41-3ddb-7d51-9a8e-6b1f2c3d4e5f"
		mainTurn  = "01a0cc41-5b10-7c22-8d33-9e4f5a6b7c8d"
		titleSID  = "01a0cc41-a647-7e10-b2c3-d4e5f6a7b8c9"
		titleTurn = "01a0cc41-a650-7f21-83d4-e5f6a7b8c9d0"
	)
	rolloutDir := filepath.Join(codexHome, "sessions", "2026", "09", "23")
	if err := os.MkdirAll(rolloutDir, 0o755); err != nil {
		t.Fatal(err)
	}
	rollout := strings.Join([]string{
		`{"timestamp":"2026-09-23T03:13:42.876Z","type":"session_meta","payload":{"session_id":"` + mainSID + `","id":"` + mainSID + `","cwd":"/tmp/sb-codex","originator":"codex-tui","cli_version":"0.155.1","source":"cli","thread_source":"user"}}`,
		`{"timestamp":"2026-09-23T03:14:09.177Z","type":"event_msg","payload":{"type":"task_started","turn_id":"` + mainTurn + `"}}`,
	}, "\n") + "\n"
	if err := os.WriteFile(filepath.Join(rolloutDir, "rollout-2026-09-23T05-13-42-"+mainSID+".jsonl"), []byte(rollout), 0o600); err != nil {
		t.Fatal(err)
	}

	storage, err := session.NewStorageWithProfile(profile)
	if err != nil {
		t.Fatalf("new storage: %v", err)
	}
	t.Cleanup(func() { _ = storage.Close() })
	projectDir := filepath.Join(home, "proj")
	if err := os.MkdirAll(projectDir, 0o755); err != nil {
		t.Fatal(err)
	}
	inst := &session.Instance{
		ID: "inst-defer-codex-title", Title: "codex-busy", Tool: "codex",
		ProjectPath: projectDir, Command: "codex", CodexSessionID: mainSID,
	}
	if err := storage.Save([]*session.Instance{inst}); err != nil {
		t.Fatalf("seed save: %v", err)
	}
	t.Setenv("AGENTDECK_INSTANCE_ID", inst.ID)

	origArgs := os.Args
	t.Cleanup(func() { os.Args = origArgs })
	notify := func(payload string) {
		os.Args = []string{"agent-deck", "codex-notify", payload}
		handleCodexNotify()
	}
	fetch := func() (string, error) { return fetchHookDrivenStatus(profile, inst.Title) }

	notify(`{"type":"turn/started","thread-id":"` + mainSID + `","turn-id":"` + mainTurn + `"}`)
	if got, err := fetch(); err != nil || !send.StatusIsBusy(got) {
		t.Fatalf("main turn started: status = %q, %v; want busy", got, err)
	}

	// The title thread finishes while the main turn is still running.
	notify(`{"type":"agent-turn-complete","thread-id":"` + titleSID + `","turn-id":"` + titleTurn +
		`","cwd":"/tmp/sb-codex","input-messages":["Generate a concise, single-line title"],"last-assistant-message":"Codex send probe"}`)

	if anchor := session.ReadHookSessionAnchor(inst.ID); anchor != mainSID {
		t.Fatalf("hook anchor = %q after title completion, want main thread %q", anchor, mainSID)
	}
	if got, err := fetch(); err != nil || !send.StatusIsBusy(got) {
		t.Fatalf("title completion mid-turn: status = %q, %v; want still busy", got, err)
	}
	if err := send.WaitUntilNotBusy(fetch, time.Nanosecond, 10*time.Millisecond, func(time.Duration) {}); err == nil {
		t.Fatal("--defer-if-busy delivered after the title completion; it must keep holding while the main turn runs")
	}

	// The main thread's own completion is the real turn-finished edge.
	notify(`{"type":"agent-turn-complete","thread-id":"` + mainSID + `","turn-id":"` + mainTurn + `"}`)
	if got, err := fetch(); err != nil || send.StatusIsBusy(got) {
		t.Fatalf("main completion: status = %q, %v; want not busy", got, err)
	}
}
