package session

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestBuildClaudeToCodexHandoffPrompt_ReadsClaudeTranscript(t *testing.T) {
	claudeDir := t.TempDir()
	t.Setenv("CLAUDE_CONFIG_DIR", claudeDir)

	project := t.TempDir()
	if resolved, err := filepath.EvalSymlinks(project); err == nil {
		project = resolved
	}
	sessionID := "11111111-2222-3333-4444-555555555555"
	transcriptDir := filepath.Join(claudeDir, "projects", ConvertToClaudeDirName(project))
	if err := os.MkdirAll(transcriptDir, 0o755); err != nil {
		t.Fatal(err)
	}
	transcript := strings.Join([]string{
		`{"type":"user","message":{"role":"user","content":[{"type":"text","text":"Remember BLUE LANTERN."}]}}`,
		`{"type":"assistant","message":{"role":"assistant","content":[{"type":"thinking","thinking":"private scratch"},{"type":"text","text":"Stored."}]}}`,
		`{"type":"user","message":{"role":"user","content":"What was the phrase?"}}`,
	}, "\n")
	if err := os.WriteFile(filepath.Join(transcriptDir, sessionID+".jsonl"), []byte(transcript), 0o644); err != nil {
		t.Fatal(err)
	}

	inst := &Instance{
		Title:           "history-test",
		ProjectPath:     project,
		Tool:            "claude",
		ClaudeSessionID: sessionID,
	}
	prompt, info, err := BuildClaudeToCodexHandoffPrompt(inst, 32000)
	if err != nil {
		t.Fatalf("BuildClaudeToCodexHandoffPrompt: %v", err)
	}

	if !strings.Contains(prompt, "BLUE LANTERN") {
		t.Fatalf("prompt missing transcript content:\n%s", prompt)
	}
	if !strings.Contains(prompt, "previous Claude session ID: "+sessionID) {
		t.Fatalf("prompt missing source session id:\n%s", prompt)
	}
	if strings.Contains(prompt, "private scratch") || strings.Contains(prompt, "[thinking]") {
		t.Fatalf("prompt leaked thinking metadata:\n%s", prompt)
	}
	if info.MessageCount != 3 || info.IncludedCount != 3 || info.Truncated {
		t.Fatalf("info = %+v, want 3 included untruncated", info)
	}
}

func TestBuildClaudeToCodexHandoffPrompt_TruncatesToTail(t *testing.T) {
	claudeDir := t.TempDir()
	t.Setenv("CLAUDE_CONFIG_DIR", claudeDir)

	project := t.TempDir()
	if resolved, err := filepath.EvalSymlinks(project); err == nil {
		project = resolved
	}
	sessionID := "aaaaaaaa-bbbb-cccc-dddd-eeeeeeeeeeee"
	transcriptDir := filepath.Join(claudeDir, "projects", ConvertToClaudeDirName(project))
	if err := os.MkdirAll(transcriptDir, 0o755); err != nil {
		t.Fatal(err)
	}
	transcript := strings.Join([]string{
		`{"type":"user","message":{"role":"user","content":[{"type":"text","text":"old context that should drop"}]}}`,
		`{"type":"assistant","message":{"role":"assistant","content":[{"type":"text","text":"latest context should remain"}]}}`,
	}, "\n")
	if err := os.WriteFile(filepath.Join(transcriptDir, sessionID+".jsonl"), []byte(transcript), 0o644); err != nil {
		t.Fatal(err)
	}

	inst := &Instance{Title: "trim-test", ProjectPath: project, Tool: "claude", ClaudeSessionID: sessionID}
	prompt, info, err := BuildClaudeToCodexHandoffPrompt(inst, 80)
	if err != nil {
		t.Fatalf("BuildClaudeToCodexHandoffPrompt: %v", err)
	}

	if strings.Contains(prompt, "old context that should drop") {
		t.Fatalf("prompt retained old context despite tight max chars:\n%s", prompt)
	}
	if !strings.Contains(prompt, "latest context should remain") {
		t.Fatalf("prompt dropped latest context:\n%s", prompt)
	}
	if !info.Truncated || info.IncludedCount != 1 {
		t.Fatalf("info = %+v, want truncated with one included", info)
	}
}

func TestBuildClaudeToCodexHandoffPrompt_ComplexClaudeContent(t *testing.T) {
	claudeDir := t.TempDir()
	t.Setenv("CLAUDE_CONFIG_DIR", claudeDir)

	project := t.TempDir()
	if resolved, err := filepath.EvalSymlinks(project); err == nil {
		project = resolved
	}
	sessionID := "bbbbbbbb-cccc-dddd-eeee-ffffffffffff"
	transcriptDir := filepath.Join(claudeDir, "projects", ConvertToClaudeDirName(project))
	if err := os.MkdirAll(transcriptDir, 0o755); err != nil {
		t.Fatal(err)
	}
	transcript := strings.Join([]string{
		`{"type":"summary","summary":"summary records are not conversation turns"}`,
		`not json`,
		`{"type":"user","message":{"role":"user","content":[{"type":"text","text":"Please inspect the repo and remember SILVER COMPASS 1782726200."}]}}`,
		`{"type":"assistant","message":{"role":"assistant","content":[{"type":"thinking","thinking":"do not leak this"},{"type":"tool_use","name":"Read","input":{"file_path":"/tmp/example.go"}},{"type":"text","text":"I will inspect the file."}]}}`,
		`{"type":"user","message":{"role":"user","content":[{"type":"tool_result","content":[{"type":"text","text":"package main\nfunc main() {}"}]}]}}`,
		`{"type":"assistant","message":{"role":"assistant","content":[{"type":"text","text":"The repo note is SILVER COMPASS 1782726200."}]}}`,
	}, "\n")
	if err := os.WriteFile(filepath.Join(transcriptDir, sessionID+".jsonl"), []byte(transcript), 0o644); err != nil {
		t.Fatal(err)
	}

	inst := &Instance{Title: "complex-test", ProjectPath: project, Tool: "claude", ClaudeSessionID: sessionID}
	prompt, info, err := BuildClaudeToCodexHandoffPrompt(inst, 32000)
	if err != nil {
		t.Fatalf("BuildClaudeToCodexHandoffPrompt: %v", err)
	}

	for _, want := range []string{
		"SILVER COMPASS 1782726200",
		"[tool_use Read]",
		`"file_path":"/tmp/example.go"`,
		"[tool_result]",
		"package main",
	} {
		if !strings.Contains(prompt, want) {
			t.Fatalf("prompt missing %q:\n%s", want, prompt)
		}
	}
	for _, unwanted := range []string{"do not leak this", "summary records", "not json"} {
		if strings.Contains(prompt, unwanted) {
			t.Fatalf("prompt included unwanted %q:\n%s", unwanted, prompt)
		}
	}
	if info.MessageCount != 4 || info.IncludedCount != 4 || info.Truncated {
		t.Fatalf("info = %+v, want 4 readable conversation turns untruncated", info)
	}
}
