package main

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/asheshgoplani/agent-deck/internal/session"
)

// seedHandoffSession creates the same persisted state that a real Claude
// session would have: a registry entry with a Claude conversation id and a
// transcript under the active HOME. Keeping this at the CLI boundary makes
// the tests cover flag parsing, session resolution, transcript lookup, and
// output formatting together.
func seedHandoffSession(t *testing.T, home string) (id, transcriptPath string) {
	t.Helper()
	project := filepath.Join(home, "handoff-project")
	if err := os.MkdirAll(project, 0o755); err != nil {
		t.Fatalf("mkdir project: %v", err)
	}

	id = addTestSession(t, home, project, "handoff-cli-test")
	const sessionID = "11111111-2222-3333-4444-555555555555"
	stdout, stderr, code := runAgentDeck(t, home,
		"session", "set", "--json", id, "claude-session-id", sessionID,
	)
	if code != 0 {
		t.Fatalf("set claude-session-id failed (exit %d)\nstdout: %s\nstderr: %s", code, stdout, stderr)
	}

	transcriptDir := filepath.Join(home, ".claude", "projects", session.ConvertToClaudeDirName(project))
	if err := os.MkdirAll(transcriptDir, 0o755); err != nil {
		t.Fatalf("mkdir transcript dir: %v", err)
	}
	transcriptPath = filepath.Join(transcriptDir, sessionID+".jsonl")
	transcript := strings.Join([]string{
		`{"type":"user","message":{"role":"user","content":"Remember HANDOFF BLUE."}}`,
		`{"type":"assistant","message":{"role":"assistant","content":"Stored HANDOFF BLUE."}}`,
		`{"type":"user","message":{"role":"user","content":"Continue from this context."}}`,
	}, "\n")
	if err := os.WriteFile(transcriptPath, []byte(transcript), 0o644); err != nil {
		t.Fatalf("write transcript: %v", err)
	}
	return id, transcriptPath
}

func TestSessionHandoffJSON_ParsesFlagsAndIncludesInfo(t *testing.T) {
	if testing.Short() {
		t.Skip("subprocess CLI test skipped in short mode")
	}
	home := t.TempDir()
	id, _ := seedHandoffSession(t, home)

	stdout, stderr, code := runAgentDeck(t, home,
		"session", "handoff", id, "--json", "--max-chars", "120",
	)
	if code != 0 {
		t.Fatalf("session handoff --json failed (exit %d)\nstdout: %s\nstderr: %s", code, stdout, stderr)
	}
	if stderr != "" {
		t.Fatalf("JSON handoff should not write a status line to stderr, got: %q", stderr)
	}

	var payload struct {
		Prompt string              `json:"prompt"`
		Info   session.HandoffInfo `json:"info"`
	}
	if err := json.Unmarshal([]byte(stdout), &payload); err != nil {
		t.Fatalf("parse handoff JSON: %v\nstdout: %s", err, stdout)
	}
	if payload.Prompt == "" || !strings.Contains(payload.Prompt, "HANDOFF BLUE") {
		t.Fatalf("handoff prompt missing transcript content: %q", payload.Prompt)
	}
	if payload.Info.MessageCount != 3 || payload.Info.IncludedCount != 3 {
		t.Fatalf("handoff info = %+v, want all 3 messages included", payload.Info)
	}
	if payload.Info.MaxChars != 120 {
		t.Fatalf("max_chars = %d, want 120", payload.Info.MaxChars)
	}
}

func TestSessionHandoffOut_WritesPromptAndProtectsTranscript(t *testing.T) {
	if testing.Short() {
		t.Skip("subprocess CLI test skipped in short mode")
	}
	home := t.TempDir()
	id, transcriptPath := seedHandoffSession(t, home)
	outPath := filepath.Join(home, "handoff.txt")

	stdout, stderr, code := runAgentDeck(t, home,
		"session", "handoff", id, "--out", outPath,
	)
	if code != 0 {
		t.Fatalf("session handoff --out failed (exit %d)\nstdout: %s\nstderr: %s", code, stdout, stderr)
	}
	if stdout != "" {
		t.Fatalf("--out should keep the prompt out of stdout, got: %q", stdout)
	}
	if !strings.Contains(stderr, "handoff:") {
		t.Fatalf("--out should report handoff metadata on stderr, got: %q", stderr)
	}

	promptBytes, err := os.ReadFile(outPath)
	if err != nil {
		t.Fatalf("read output file: %v", err)
	}
	if !strings.Contains(string(promptBytes), "HANDOFF BLUE") {
		t.Fatalf("output file missing transcript content: %q", promptBytes)
	}

	stdout, stderr, code = runAgentDeck(t, home,
		"session", "handoff", id, "--out", transcriptPath,
	)
	if code != 1 {
		t.Fatalf("expected overwrite guard to exit 1, got %d\nstdout: %s\nstderr: %s", code, stdout, stderr)
	}
	if !strings.Contains(stderr, "--out refuses to overwrite the source transcript") {
		t.Fatalf("overwrite guard message missing, got: %q", stderr)
	}

	linkPath := filepath.Join(home, "handoff-link.txt")
	if err := os.Symlink(transcriptPath, linkPath); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
	beforeTranscript, err := os.ReadFile(transcriptPath)
	if err != nil {
		t.Fatalf("read source transcript before symlink guard: %v", err)
	}
	stdout, stderr, code = runAgentDeck(t, home,
		"session", "handoff", id, "--out", linkPath,
	)
	if code != 1 {
		t.Fatalf("expected symlink overwrite guard to exit 1, got %d\nstdout: %s\nstderr: %s", code, stdout, stderr)
	}
	if !strings.Contains(stderr, "--out refuses to overwrite the source transcript") {
		t.Fatalf("symlink overwrite guard message missing, got: %q", stderr)
	}
	afterTranscript, err := os.ReadFile(transcriptPath)
	if err != nil {
		t.Fatalf("read source transcript after symlink guard: %v", err)
	}
	if !bytes.Equal(afterTranscript, beforeTranscript) {
		t.Fatalf("symlink overwrite guard changed the source transcript")
	}
}

func TestSamePath_ResolvesExistingSymlinks(t *testing.T) {
	target := filepath.Join(t.TempDir(), "transcript.jsonl")
	if err := os.WriteFile(target, []byte("fixture"), 0o600); err != nil {
		t.Fatalf("write target: %v", err)
	}
	link := filepath.Join(filepath.Dir(target), "alias.jsonl")
	if err := os.Symlink(target, link); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
	if !samePath(target, link) {
		t.Fatalf("samePath(%q, %q) = false, want true", target, link)
	}
}
