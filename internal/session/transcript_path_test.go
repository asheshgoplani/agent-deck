package session

import (
	"os"
	"path/filepath"
	"testing"
)

// mkTranscript creates <cfg>/projects/<dir>/<id>.jsonl and returns its path.
func mkTranscript(t *testing.T, cfg, dir, id string) string {
	t.Helper()
	full := filepath.Join(cfg, "projects", dir)
	if err := os.MkdirAll(full, 0o755); err != nil {
		t.Fatal(err)
	}
	p := filepath.Join(full, id+".jsonl")
	if err := os.WriteFile(p, []byte("{}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	return p
}

func TestResolveClaudeTranscriptPath_PrimaryHit(t *testing.T) {
	cfg := t.TempDir()
	proj := "/home/user/proj"
	id := "11111111-2222-3333-4444-555555555555"
	want := mkTranscript(t, cfg, ConvertToClaudeDirName(proj), id)

	if got := resolveClaudeTranscriptPath(cfg, proj, id); got != want {
		t.Fatalf("primary hit: got %q want %q", got, want)
	}
}

// The transcript lives under the Windows/UNC-derived directory name that Claude
// Code (running Windows-native) creates, which differs from the name agent-deck
// computes from the stored WSL Linux path. The glob fallback must still find it.
func TestResolveClaudeTranscriptPath_GlobFallbackWSL(t *testing.T) {
	cfg := t.TempDir()
	proj := "/home/user/proj" // computes to -home-user-proj
	id := "aaaaaaaa-bbbb-cccc-dddd-eeeeeeeeeeee"
	// Actual dir as Claude would name \\wsl.localhost\Ubuntu\home\user\proj
	want := mkTranscript(t, cfg, "--wsl-localhost-Ubuntu-home-user-proj", id)

	if got := resolveClaudeTranscriptPath(cfg, proj, id); got != want {
		t.Fatalf("glob fallback: got %q want %q", got, want)
	}
}

func TestResolveClaudeTranscriptPath_NotFound(t *testing.T) {
	cfg := t.TempDir()
	mkTranscript(t, cfg, "some-dir", "11111111-2222-3333-4444-555555555555")

	if got := resolveClaudeTranscriptPath(cfg, "/home/user/proj", "no-such-session-id"); got != "" {
		t.Fatalf("missing id: expected empty, got %q", got)
	}
	if got := resolveClaudeTranscriptPath(cfg, "/home/user/proj", ""); got != "" {
		t.Fatalf("empty id: expected empty, got %q", got)
	}
}
