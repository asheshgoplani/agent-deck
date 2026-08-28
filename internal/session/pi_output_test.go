package session

import (
	"os"
	"path/filepath"
	"testing"
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
