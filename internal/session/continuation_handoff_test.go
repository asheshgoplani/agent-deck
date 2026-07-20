package session

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// writeHandoffFixture stands up a Claude transcript on disk and returns an
// Instance pointing at it. Mirrors the setup in handoff_test.go.
func writeHandoffFixture(t *testing.T, lines ...string) *Instance {
	t.Helper()
	claudeDir := t.TempDir()
	t.Setenv("CLAUDE_CONFIG_DIR", claudeDir)

	project := t.TempDir()
	if resolved, err := filepath.EvalSymlinks(project); err == nil {
		project = resolved
	}
	sessionID := "99999999-8888-7777-6666-555555555555"
	transcriptDir := filepath.Join(claudeDir, "projects", ConvertToClaudeDirName(project))
	if err := os.MkdirAll(transcriptDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(transcriptDir, sessionID+".jsonl"), []byte(strings.Join(lines, "\n")), 0o644); err != nil {
		t.Fatal(err)
	}
	return &Instance{
		Title:           "continuation-test",
		ProjectPath:     project,
		Tool:            "claude",
		ClaudeSessionID: sessionID,
	}
}

// The UI writes the curated prompt and the CLI reads it, so both must agree on
// where it lives — a divergence would silently make `session handoff` ignore
// every wrap-up the agent ever wrote.
func TestHandoffPromptPath(t *testing.T) {
	got := HandoffPromptPath("sess-42")
	if got == "" {
		t.Fatal("HandoffPromptPath returned empty")
	}
	wantSuffix := filepath.Join("handoff", "sess-42", "PROMPT.md")
	if !strings.HasSuffix(got, wantSuffix) {
		t.Errorf("HandoffPromptPath = %q, want suffix %q", got, wantSuffix)
	}
	if HandoffPromptPath("") != "" {
		t.Error("empty id should yield empty path, not a directory-level path")
	}
}

// The curated agent-authored PROMPT.md is the higher-quality artifact, so it
// wins whenever it exists.
func TestResolveContinuationPrompt_PrefersAgentPrompt(t *testing.T) {
	inst := writeHandoffFixture(t,
		`{"type":"user","message":{"role":"user","content":[{"type":"text","text":"TRANSCRIPT MARKER"}]}}`,
	)
	promptPath := filepath.Join(t.TempDir(), "PROMPT.md")
	if err := os.WriteFile(promptPath, []byte("CURATED MARKER"), 0o644); err != nil {
		t.Fatal(err)
	}

	got, err := ResolveContinuationPrompt(inst, "claude", promptPath, 32000)
	if err != nil {
		t.Fatalf("ResolveContinuationPrompt: %v", err)
	}
	if got.Source != "agent" {
		t.Errorf("Source = %q, want \"agent\"", got.Source)
	}
	if !strings.Contains(got.Text, "CURATED MARKER") {
		t.Errorf("did not use the curated prompt:\n%s", got.Text)
	}
	if strings.Contains(got.Text, "TRANSCRIPT MARKER") {
		t.Errorf("curated prompt should not carry the transcript:\n%s", got.Text)
	}
}

// The whole point of the fallback: a missing PROMPT.md must still yield a
// usable continuation prompt rather than an error.
func TestResolveContinuationPrompt_FallsBackToTranscript(t *testing.T) {
	inst := writeHandoffFixture(t,
		`{"type":"user","message":{"role":"user","content":[{"type":"text","text":"TRANSCRIPT MARKER"}]}}`,
	)
	missing := filepath.Join(t.TempDir(), "PROMPT.md")

	got, err := ResolveContinuationPrompt(inst, "claude", missing, 32000)
	if err != nil {
		t.Fatalf("ResolveContinuationPrompt: %v", err)
	}
	if got.Source != "transcript" {
		t.Errorf("Source = %q, want \"transcript\"", got.Source)
	}
	if !strings.Contains(got.Text, "TRANSCRIPT MARKER") {
		t.Errorf("fallback did not carry the transcript:\n%s", got.Text)
	}
}

// An empty PROMPT.md is the agent having created the file but written nothing
// (a wrap-up that died mid-write). Treat it as absent, not as a valid handoff.
func TestResolveContinuationPrompt_EmptyAgentPromptFallsBack(t *testing.T) {
	inst := writeHandoffFixture(t,
		`{"type":"user","message":{"role":"user","content":[{"type":"text","text":"TRANSCRIPT MARKER"}]}}`,
	)
	promptPath := filepath.Join(t.TempDir(), "PROMPT.md")
	if err := os.WriteFile(promptPath, []byte("   \n\t\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	got, err := ResolveContinuationPrompt(inst, "claude", promptPath, 32000)
	if err != nil {
		t.Fatalf("ResolveContinuationPrompt: %v", err)
	}
	if got.Source != "transcript" {
		t.Errorf("Source = %q, want \"transcript\" for a whitespace-only PROMPT.md", got.Source)
	}
}

// Only when BOTH sources are unusable may the resolver fail — that error is the
// sole remaining trigger for a failsafe pause.
func TestResolveContinuationPrompt_ErrorsWhenBothUnusable(t *testing.T) {
	inst := writeHandoffFixture(t) // transcript file exists but has no records
	missing := filepath.Join(t.TempDir(), "PROMPT.md")

	if _, err := ResolveContinuationPrompt(inst, "claude", missing, 32000); err == nil {
		t.Fatal("expected an error when neither a curated prompt nor a transcript is usable")
	}
}

// A cross-tool handoff must name the target runtime so the receiving agent
// knows it is not the tool that produced the transcript.
func TestBuildContinuationHandoffPrompt_CrossToolNamesTarget(t *testing.T) {
	inst := writeHandoffFixture(t,
		`{"type":"user","message":{"role":"user","content":[{"type":"text","text":"Remember BLUE LANTERN."}]}}`,
	)

	prompt, _, err := BuildContinuationHandoffPrompt(inst, "codex", 32000)
	if err != nil {
		t.Fatalf("BuildContinuationHandoffPrompt: %v", err)
	}
	if !strings.Contains(prompt, "BLUE LANTERN") {
		t.Fatalf("prompt missing transcript content:\n%s", prompt)
	}
	if !strings.Contains(strings.ToLower(prompt), "codex") {
		t.Fatalf("cross-tool prompt does not name the target tool:\n%s", prompt)
	}
}

// A same-tool continuation is not a cross-tool handoff: it must not tell the
// agent it was handed to a different runtime, which would be a lie that
// changes how the agent reasons about its own prior messages.
func TestBuildContinuationHandoffPrompt_SameToolDoesNotClaimCrossTool(t *testing.T) {
	inst := writeHandoffFixture(t,
		`{"type":"user","message":{"role":"user","content":[{"type":"text","text":"Remember BLUE LANTERN."}]}}`,
	)

	prompt, _, err := BuildContinuationHandoffPrompt(inst, "claude", 32000)
	if err != nil {
		t.Fatalf("BuildContinuationHandoffPrompt: %v", err)
	}
	if !strings.Contains(prompt, "BLUE LANTERN") {
		t.Fatalf("prompt missing transcript content:\n%s", prompt)
	}
	if strings.Contains(strings.ToLower(prompt), "codex") {
		t.Fatalf("same-tool continuation claims a handoff to Codex:\n%s", prompt)
	}
}
