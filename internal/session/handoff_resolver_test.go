package session

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// writeHandoffTranscript seeds a one-line Claude transcript under
// <claudeDir>/projects/<projectDirName>/<sessionID>.jsonl and returns its path.
func writeHandoffTranscript(t *testing.T, claudeDir, projectDirName, sessionID, text string) string {
	t.Helper()
	dir := filepath.Join(claudeDir, "projects", projectDirName)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, sessionID+".jsonl")
	line := `{"type":"user","message":{"role":"user","content":"` + text + `"}}`
	if err := os.WriteFile(path, []byte(line), 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}

// Issue #1671: locateHandoffTranscript duplicated the resolver's stat probes and
// therefore lacked resolveClaudeTranscriptPath's UUID-glob fallback. When Claude
// encodes the project dir differently from agent-deck's stored ProjectPath —
// the WSL case, where Claude Code runs Windows-native and names the dir from the
// UNC/Windows cwd — every computed path missed and `session handoff` failed with
// "read Claude transcript: no such file". The session ID is a UUID, so matching
// on the filename anywhere under projects/ is unambiguous.
//
// Fails without the fix: locateHandoffTranscript returns the (nonexistent)
// canonical path and BuildClaudeToCodexHandoffPrompt errors.
func TestLocateHandoffTranscript_UUIDGlobFallbackForForeignProjectEncoding(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	claudeDir := t.TempDir()
	t.Setenv("CLAUDE_CONFIG_DIR", claudeDir)
	ClearUserConfigCache()
	t.Cleanup(ClearUserConfigCache)

	project := t.TempDir()
	if resolved, err := filepath.EvalSymlinks(project); err == nil {
		project = resolved
	}
	sessionID := "9f2c1d4e-1111-4222-8333-444455556666"

	// The transcript lives under the Windows/UNC-derived directory name, which
	// can never equal ConvertToClaudeDirName(project).
	want := writeHandoffTranscript(t, claudeDir,
		"--wsl-localhost-Ubuntu-home-user-proj", sessionID, "WSL ENCODED TRANSCRIPT")
	if want == claudeTranscriptPathIn(claudeDir, &Instance{ProjectPath: project}, sessionID) {
		t.Fatal("test setup is degenerate: foreign encoding equals the canonical path")
	}

	inst := &Instance{Title: "wsl-handoff", ProjectPath: project, Tool: "claude", ClaudeSessionID: sessionID}

	if got := locateHandoffTranscript(inst); got != want {
		t.Fatalf("locateHandoffTranscript = %q, want the glob-matched transcript %q", got, want)
	}

	prompt, info, err := BuildClaudeToCodexHandoffPrompt(inst, DefaultHandoffMaxChars)
	if err != nil {
		t.Fatalf("BuildClaudeToCodexHandoffPrompt: %v", err)
	}
	if !strings.Contains(prompt, "WSL ENCODED TRANSCRIPT") {
		t.Fatalf("prompt missing transcript content:\n%s", prompt)
	}
	if info.TranscriptPath != want {
		t.Fatalf("info.TranscriptPath = %q, want %q", info.TranscriptPath, want)
	}
}

// The canonical (EffectiveWorkingDir) encoding must still win over a stray
// glob match, so a multi-repo session (issue #663) keeps reading its own
// transcript rather than whichever directory Glob happens to return first.
func TestLocateHandoffTranscript_PrefersCanonicalEncodingOverGlob(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	claudeDir := t.TempDir()
	t.Setenv("CLAUDE_CONFIG_DIR", claudeDir)
	ClearUserConfigCache()
	t.Cleanup(ClearUserConfigCache)

	project := t.TempDir()
	if resolved, err := filepath.EvalSymlinks(project); err == nil {
		project = resolved
	}
	sessionID := "7a1b2c3d-5555-4666-8777-888899990000"

	inst := &Instance{Title: "canonical-wins", ProjectPath: project, Tool: "claude", ClaudeSessionID: sessionID}
	canonical := claudeTranscriptPathIn(claudeDir, inst, sessionID)

	// A same-UUID decoy sorted before the canonical dir name (Glob returns
	// lexically sorted matches, and "-" sorts below alphanumerics).
	writeHandoffTranscript(t, claudeDir, "--decoy-sorts-first", sessionID, "DECOY")
	if err := os.MkdirAll(filepath.Dir(canonical), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(canonical,
		[]byte(`{"type":"user","message":{"role":"user","content":"CANONICAL"}}`), 0o644); err != nil {
		t.Fatal(err)
	}

	if got := locateHandoffTranscript(inst); got != canonical {
		t.Fatalf("locateHandoffTranscript = %q, want canonical %q", got, canonical)
	}
}

// With no transcript anywhere, the resolver must report the canonical expected
// path so the CLI error names where the transcript was looked for.
func TestLocateHandoffTranscript_FallsBackToExpectedPathWhenMissing(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	claudeDir := t.TempDir()
	t.Setenv("CLAUDE_CONFIG_DIR", claudeDir)
	ClearUserConfigCache()
	t.Cleanup(ClearUserConfigCache)

	project := t.TempDir()
	if resolved, err := filepath.EvalSymlinks(project); err == nil {
		project = resolved
	}
	inst := &Instance{
		Title:           "missing-transcript",
		ProjectPath:     project,
		Tool:            "claude",
		ClaudeSessionID: "0d0d0d0d-9999-4aaa-8bbb-cccccccccccc",
	}

	want := ClaudeTranscriptPathForInstance(inst)
	if got := locateHandoffTranscript(inst); got != want {
		t.Fatalf("locateHandoffTranscript = %q, want expected-path fallback %q", got, want)
	}
}

// resolveHandoffTranscriptIn is the shared per-config-dir probe; guard its
// degenerate inputs so a nil instance or empty session id can never turn into a
// glob over every project directory.
func TestResolveHandoffTranscriptIn_RejectsDegenerateInputs(t *testing.T) {
	claudeDir := t.TempDir()
	inst := &Instance{ProjectPath: t.TempDir(), Tool: "claude", ClaudeSessionID: "abc"}

	if got := resolveHandoffTranscriptIn("", inst, "abc"); got != "" {
		t.Fatalf("empty configDir returned %q", got)
	}
	if got := resolveHandoffTranscriptIn(claudeDir, nil, "abc"); got != "" {
		t.Fatalf("nil instance returned %q", got)
	}
	if got := resolveHandoffTranscriptIn(claudeDir, inst, ""); got != "" {
		t.Fatalf("empty sessionID returned %q", got)
	}
}
