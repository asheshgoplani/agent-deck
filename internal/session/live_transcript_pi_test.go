package session

import (
	"os"
	"path/filepath"
	"testing"
)

func writePiSessionDoc(t *testing.T, home, instID, name, id, cwd string) string {
	t.Helper()
	dir := filepath.Join(home, ".pi", "agent-deck", instID)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, name)
	if err := os.WriteFile(path, []byte(`{"type":"session","version":3,"id":"`+id+`","cwd":"`+cwd+`"}`+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

// A Pi session's live transcript is its exact session document, so the
// status bus, the send queue and the rows reader all find it.
func TestLiveTranscriptPathPi(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	project := filepath.Join(home, "project")
	inst := &Instance{ID: "pi-live", Tool: "pi", ProjectPath: project}
	if got := LiveTranscriptPath(inst, nil); got != "" {
		t.Fatalf("no session document yet: got %q", got)
	}
	first := writePiSessionDoc(t, home, inst.ID, "2026-11-16T00-00-00-000Z_a.jsonl", "pi-a", project)
	if got := LiveTranscriptPath(inst, nil); got != first {
		t.Fatalf("single document: got %q, want %q", got, first)
	}
	writePiSessionDoc(t, home, inst.ID, "2026-11-16T00-01-00-000Z_b.jsonl", "pi-b", project)
	if got := LiveTranscriptPath(inst, nil); got != "" {
		t.Fatalf("two documents and no persisted identity is ambiguous: got %q", got)
	}
	inst.PiSessionID, inst.PiSessionPath = "pi-a", first
	if got := LiveTranscriptPath(inst, nil); got != first {
		t.Fatalf("persisted identity: got %q, want %q", got, first)
	}
	foreign := &Instance{ID: "pi-foreign", Tool: "pi", ProjectPath: project}
	writePiSessionDoc(t, home, foreign.ID, "x.jsonl", "pi-x", "/somewhere/else")
	if got := LiveTranscriptPath(foreign, nil); got != "" {
		t.Fatalf("a document for another directory: got %q", got)
	}
}

// BenchmarkLiveTranscriptPath times the per-session lookup the status bus
// runs on each pass, for Claude and for a Pi session with a persisted
// identity.
func BenchmarkLiveTranscriptPath(b *testing.B) {
	home := b.TempDir()
	b.Setenv("HOME", home)
	b.Setenv("CLAUDE_CONFIG_DIR", filepath.Join(home, ".claude"))
	project := filepath.Join(home, "project")
	claude := &Instance{ID: "c1", Tool: "claude", ProjectPath: project, ClaudeSessionID: "conv-1"}
	cpath := ClaudeTranscriptPathForInstance(claude)
	_ = os.MkdirAll(filepath.Dir(cpath), 0o700)
	_ = os.WriteFile(cpath, []byte("{}\n"), 0o600)
	pi := &Instance{ID: "pi-bench", Tool: "pi", ProjectPath: project}
	dir := filepath.Join(home, ".pi", "agent-deck", pi.ID)
	_ = os.MkdirAll(dir, 0o700)
	ppath := filepath.Join(dir, "s.jsonl")
	_ = os.WriteFile(ppath, []byte(`{"type":"session","version":3,"id":"pi-s","cwd":"`+project+`"}`+"\n"), 0o600)
	pi.PiSessionID, pi.PiSessionPath = "pi-s", ppath
	for _, inst := range []*Instance{claude, pi} {
		b.Run(inst.Tool, func(b *testing.B) {
			for i := 0; i < b.N; i++ {
				if LiveTranscriptPath(inst, nil) == "" {
					b.Fatal("no path")
				}
			}
		})
	}
}
