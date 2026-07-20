package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/asheshgoplani/agent-deck/internal/session"
)

// handoffFixture stands up an isolated data/config layout plus a Claude
// transcript on disk, and returns an Instance whose ID drives
// session.HandoffPromptPath. Mirrors writeHandoffFixture in the session
// package but also pins XDG_DATA_HOME/HOME so HandoffPromptPath(inst.ID)
// resolves under a temp dir the test controls.
func handoffFixture(t *testing.T, transcriptLines ...string) *session.Instance {
	t.Helper()

	t.Setenv("HOME", t.TempDir())
	t.Setenv("XDG_DATA_HOME", t.TempDir())

	claudeDir := t.TempDir()
	t.Setenv("CLAUDE_CONFIG_DIR", claudeDir)

	project := t.TempDir()
	if resolved, err := filepath.EvalSymlinks(project); err == nil {
		project = resolved
	}
	claudeSessionID := "11111111-2222-3333-4444-555555555555"
	transcriptDir := filepath.Join(claudeDir, "projects", session.ConvertToClaudeDirName(project))
	if err := os.MkdirAll(transcriptDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(transcriptDir, claudeSessionID+".jsonl"), []byte(strings.Join(transcriptLines, "\n")), 0o644); err != nil {
		t.Fatal(err)
	}

	return &session.Instance{
		ID:              "handoff-cli-test-id",
		Title:           "handoff-cli-test",
		ProjectPath:     project,
		Tool:            "claude",
		ClaudeSessionID: claudeSessionID,
	}
}

// writeCuratedPrompt drops a PROMPT.md at exactly the path the seam will
// consult for inst — i.e. HandoffPromptPath(inst.ID). Writing it anywhere
// else would not exercise the wiring under test.
func writeCuratedPrompt(t *testing.T, inst *session.Instance, body string) {
	t.Helper()
	p := session.HandoffPromptPath(inst.ID)
	if p == "" {
		t.Fatal("HandoffPromptPath returned empty; fixture env not isolated")
	}
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

// An invalid --target-tool must be rejected before any prompt work, so the CLI
// fails fast with a clear error instead of silently degrading the handoff.
func TestResolveSessionHandoff_InvalidTargetToolErrors(t *testing.T) {
	inst := handoffFixture(t,
		`{"type":"user","message":{"role":"user","content":[{"type":"text","text":"marker"}]}}`,
	)

	if _, err := resolveSessionHandoff(inst, "no-such-tool-xyz", false, 32000); err == nil {
		t.Fatal("expected an error for an unknown target tool")
	}
}

// The seam must compute the curated-prompt path from inst.ID via
// HandoffPromptPath. If it looked anywhere else, a real wrap-up would be
// ignored — so a PROMPT.md placed at exactly that path must win.
func TestResolveSessionHandoff_UsesCuratedPromptAtComputedPath(t *testing.T) {
	inst := handoffFixture(t,
		`{"type":"user","message":{"role":"user","content":[{"type":"text","text":"TRANSCRIPT MARKER"}]}}`,
	)
	writeCuratedPrompt(t, inst, "CURATED MARKER")

	got, err := resolveSessionHandoff(inst, "codex", false, 32000)
	if err != nil {
		t.Fatalf("resolveSessionHandoff: %v", err)
	}
	if got.Source != session.ContinuationSourceAgent {
		t.Errorf("Source = %q, want %q", got.Source, session.ContinuationSourceAgent)
	}
	if !strings.Contains(got.Text, "CURATED MARKER") {
		t.Errorf("did not use the curated prompt:\n%s", got.Text)
	}
}

// --ignore-agent-prompt exists to force a rebuild from the transcript. Its
// whole job is to blank the curated-prompt path, so even with a PROMPT.md
// present the source must be the transcript.
func TestResolveSessionHandoff_IgnoreAgentPromptForcesTranscript(t *testing.T) {
	inst := handoffFixture(t,
		`{"type":"user","message":{"role":"user","content":[{"type":"text","text":"TRANSCRIPT MARKER"}]}}`,
	)
	writeCuratedPrompt(t, inst, "CURATED MARKER")

	got, err := resolveSessionHandoff(inst, "codex", true, 32000)
	if err != nil {
		t.Fatalf("resolveSessionHandoff: %v", err)
	}
	if got.Source != session.ContinuationSourceTranscript {
		t.Errorf("Source = %q, want %q (ignore-agent-prompt must bypass the curated file)", got.Source, session.ContinuationSourceTranscript)
	}
	if !strings.Contains(got.Text, "TRANSCRIPT MARKER") {
		t.Errorf("fallback did not carry the transcript:\n%s", got.Text)
	}
	if strings.Contains(got.Text, "CURATED MARKER") {
		t.Errorf("ignore-agent-prompt still used the curated file:\n%s", got.Text)
	}
}
