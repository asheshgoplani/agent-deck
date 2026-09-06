package session

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/asheshgoplani/agent-deck/internal/tmux"
)

func TestPiLastResponseReadsStructuredTranscript(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	dir := filepath.Join(home, ".pi", "agent-deck", "deck-session")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	data := []byte(`{"type":"session","id":"pi-session","timestamp":"2026-08-28T06:36:55.364Z"}
{"type":"message","timestamp":"2026-08-28T06:41:18.413Z","message":{"role":"user","content":[{"type":"text","text":"Hello"}]}}
{"type":"message","timestamp":"2026-08-28T06:41:23.295Z","message":{"role":"assistant","content":[{"type":"thinking","thinking":"brief"},{"type":"text","text":"Hello, world!"}]}}
`)
	if err := os.WriteFile(filepath.Join(dir, "session.jsonl"), data, 0o600); err != nil {
		t.Fatal(err)
	}

	got, err := (&Instance{ID: "deck-session", Tool: "pi"}).GetLastResponse()
	if err != nil {
		t.Fatal(err)
	}
	if got.Tool != "pi" || got.Role != "assistant" || got.Content != "Hello, world!" ||
		got.Timestamp != "2026-08-28T06:41:23.295Z" || got.SessionID != "pi-session" {
		t.Fatalf("unexpected response: %+v", got)
	}
}

// A legacy SSH instance's transcript belongs to the target HOME, even when a
// stale local directory happens to carry the same instance ID.
func TestPiLastResponseNonlocalRefusesLocalTranscript(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	dir := filepath.Join(home, ".pi", "agent-deck", "remote-session")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	data := []byte(`{"type":"message","timestamp":"2026-08-28T06:41:23.295Z","message":{"role":"assistant","content":[{"type":"text","text":"WRONG LOCAL RESPONSE"}]}}`)
	if err := os.WriteFile(filepath.Join(dir, "session.jsonl"), data, 0o600); err != nil {
		t.Fatal(err)
	}
	// The transport boundary returns a different pane response, never the
	// controller's stale transcript. No SSH network access is needed here.
	bin := t.TempDir()
	if err := os.WriteFile(filepath.Join(bin, "tmux"), []byte("#!/bin/sh\nprintf 'REMOTE PANE RESPONSE\\n'\n"), 0700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	for _, inst := range []*Instance{
		{ID: "remote-session", Tool: "pi", SSHHost: "remote.example"},
		{ID: "remote-session", Tool: "pi", Sandbox: &SandboxConfig{Enabled: true}},
	} {
		if got, err := inst.GetLastResponse(); err == nil {
			t.Fatalf("read local transcript for nonlocal instance: %+v", got)
		}
		if got, err := inst.GetLastResponseBestEffort(); err == nil {
			t.Fatalf("best effort read local transcript for nonlocal instance without a pane: %+v", got)
		}
		inst.tmuxSession = tmux.NewSession("pi-nonlocal-output", home)
		got, err := inst.GetLastResponseBestEffort()
		if err != nil {
			t.Fatal(err)
		}
		if got.Content != "REMOTE PANE RESPONSE" || got.Timestamp != "" {
			t.Fatalf("nonlocal output must come from the pane without a timestamp: %+v", got)
		}
	}
}

func TestPiResponseParserHandlesPartialAndNontextRecords(t *testing.T) {
	data := []byte(`{"type":"session","id":"pi-selected"}
{"type":"message","timestamp":"2026-08-28T06:41:23.295Z","message":{"role":"assistant","content":[{"type":"text","text":"first"},{"type":"text","text":"second"}]}}
{"type":"message","timestamp":"2026-08-28T06:41:24Z","message":{"role":"assistant","content":[{"type":"thinking","thinking":"hidden"},{"type":"toolCall","name":"read"}]}}
{"type":"message","message":{"role":"user","content":[{"type":"text","text":"later user"}]}}
{"type":"message",`)
	got, err := parsePiLastAssistantMessage(data)
	if err != nil {
		t.Fatal(err)
	}
	if got.Content != "first\nsecond" || got.Timestamp != "2026-08-28T06:41:23.295Z" || got.SessionID != "pi-selected" {
		t.Fatalf("unexpected parsed response: %+v", got)
	}
	if got, err := parsePiLastAssistantMessage([]byte(`{"type":"session","id":"no-replies"}`)); err == nil || got != nil {
		t.Fatalf("missing assistant must fail: %+v, %v", got, err)
	}
}
