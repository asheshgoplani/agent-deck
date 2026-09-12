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

func TestBuildClaudeToCodexHandoffPrompt_RejectsDifferentlyEncodedTranscript(t *testing.T) {
	claudeDir := t.TempDir()
	t.Setenv("CLAUDE_CONFIG_DIR", claudeDir)

	project := "/home/user/proj"
	sessionID := "eeeeeeee-ffff-0000-1111-222222222222"
	transcriptDir := filepath.Join(claudeDir, "projects", "--wsl-localhost-Ubuntu-home-user-proj")
	if err := os.MkdirAll(transcriptDir, 0o755); err != nil {
		t.Fatal(err)
	}
	transcriptPath := filepath.Join(transcriptDir, sessionID+".jsonl")
	transcript := `{"type":"user","message":{"role":"user","content":"WSL fallback found me"}}`
	if err := os.WriteFile(transcriptPath, []byte(transcript), 0o644); err != nil {
		t.Fatal(err)
	}

	inst := &Instance{
		Title:           "wsl-handoff",
		ProjectPath:     project,
		Tool:            "claude",
		ClaudeSessionID: sessionID,
	}
	_, _, err := BuildClaudeToCodexHandoffPrompt(inst, DefaultHandoffMaxChars)
	if err == nil || !strings.Contains(err.Error(), "no exact context artifact") {
		t.Fatalf("differently encoded transcript error = %v, want exact-path refusal", err)
	}
}

func TestBuildClaudeToCodexHandoffPrompt_UsesExactSourceAccountWhenIDExistsInTwoAccounts(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))
	ClearUserConfigCache()
	t.Cleanup(ClearUserConfigCache)

	project := t.TempDir()
	if resolved, err := filepath.EvalSymlinks(project); err == nil {
		project = resolved
	}
	const sessionID = "12121212-2222-3333-4444-555555555555"
	personalDir, workDir := filepath.Join(home, "claude-personal"), filepath.Join(home, "claude-work")
	cfg := &UserConfig{Profiles: map[string]ProfileSettings{
		"personal": {Claude: ProfileClaudeSettings{ConfigDir: personalDir}},
		"work":     {Claude: ProfileClaudeSettings{ConfigDir: workDir}},
	}}
	if err := SaveUserConfig(cfg); err != nil {
		t.Fatal(err)
	}
	ClearUserConfigCache()
	for dir, text := range map[string]string{personalDir: "PERSONAL EXACT SOURCE", workDir: "WRONG WORK ACCOUNT"} {
		path := filepath.Join(dir, "projects", ConvertToClaudeDirName(project), sessionID+".jsonl")
		if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(`{"sessionId":"`+sessionID+`","type":"user","message":{"role":"user","content":"`+text+`"}}`+"\n"), 0o600); err != nil {
			t.Fatal(err)
		}
	}

	prompt, info, err := BuildClaudeToCodexHandoffPrompt(&Instance{Title: "account-bound", ProjectPath: project, Tool: "claude", Account: "personal", ClaudeSessionID: sessionID}, DefaultHandoffMaxChars)
	if err != nil {
		t.Fatalf("BuildClaudeToCodexHandoffPrompt: %v", err)
	}
	if !strings.Contains(prompt, "PERSONAL EXACT SOURCE") || strings.Contains(prompt, "WRONG WORK ACCOUNT") {
		t.Fatalf("handoff used another account's same-ID transcript:\n%s", prompt)
	}
	if !strings.HasPrefix(info.TranscriptPath, personalDir+string(os.PathSeparator)) {
		t.Fatalf("TranscriptPath = %q, want personal account path", info.TranscriptPath)
	}
}

func TestBuildClaudeToCodexHandoffPrompt_MissingSourceDoesNotFallBackToAnotherAccount(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))
	ClearUserConfigCache()
	t.Cleanup(ClearUserConfigCache)

	project := t.TempDir()
	const sessionID = "13131313-2222-3333-4444-555555555555"
	personalDir, workDir := filepath.Join(home, "claude-personal"), filepath.Join(home, "claude-work")
	cfg := &UserConfig{Profiles: map[string]ProfileSettings{
		"personal": {Claude: ProfileClaudeSettings{ConfigDir: personalDir}},
		"work":     {Claude: ProfileClaudeSettings{ConfigDir: workDir}},
	}}
	if err := SaveUserConfig(cfg); err != nil {
		t.Fatal(err)
	}
	ClearUserConfigCache()
	other := filepath.Join(workDir, "projects", ConvertToClaudeDirName(project), sessionID+".jsonl")
	if err := os.MkdirAll(filepath.Dir(other), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(other, []byte(`{"sessionId":"`+sessionID+`","type":"user","message":{"role":"user","content":"must not be borrowed"}}`+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	_, _, err := BuildClaudeToCodexHandoffPrompt(&Instance{Title: "missing-source", ProjectPath: project, Tool: "claude", Account: "personal", ClaudeSessionID: sessionID}, DefaultHandoffMaxChars)
	if err == nil || !strings.Contains(err.Error(), "no exact context artifact") {
		t.Fatalf("missing personal source error = %v, want exact-source refusal", err)
	}
}

func TestBuildClaudeToCodexHandoffPrompt_RejectsForeignNativeIdentity(t *testing.T) {
	claudeDir := t.TempDir()
	t.Setenv("CLAUDE_CONFIG_DIR", claudeDir)
	project := t.TempDir()
	const sessionID = "16161616-2222-3333-4444-555555555555"
	path := filepath.Join(claudeDir, "projects", ConvertToClaudeDirName(project), sessionID+".jsonl")
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(`{"sessionId":"foreign-session","type":"user","message":{"role":"user","content":"wrong identity"}}`+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	_, _, err := BuildClaudeToCodexHandoffPrompt(&Instance{Title: "foreign", ProjectPath: project, Tool: "claude", ClaudeSessionID: sessionID}, DefaultHandoffMaxChars)
	if err == nil || !strings.Contains(err.Error(), "session identity mismatch") {
		t.Fatalf("foreign native identity error = %v, want refusal", err)
	}
}

func TestBuildClaudeToCodexHandoffPrompt_RejectsSymlinkedExactSource(t *testing.T) {
	claudeDir := t.TempDir()
	t.Setenv("CLAUDE_CONFIG_DIR", claudeDir)
	project := t.TempDir()
	const sessionID = "15151515-2222-3333-4444-555555555555"
	path := filepath.Join(claudeDir, "projects", ConvertToClaudeDirName(project), sessionID+".jsonl")
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	target := filepath.Join(t.TempDir(), "other.jsonl")
	if err := os.WriteFile(target, []byte(`{"sessionId":"`+sessionID+`"}`+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, path); err != nil {
		t.Fatal(err)
	}
	_, _, err := BuildClaudeToCodexHandoffPrompt(&Instance{Title: "symlink", ProjectPath: project, Tool: "claude", ClaudeSessionID: sessionID}, DefaultHandoffMaxChars)
	if err == nil || !strings.Contains(err.Error(), "unsafe exact Claude source path") {
		t.Fatalf("symlink source error = %v, want refusal", err)
	}
}

func TestBuildClaudeToCodexHandoffPrompt_RejectsMalformedNonTailRecord(t *testing.T) {
	claudeDir := t.TempDir()
	t.Setenv("CLAUDE_CONFIG_DIR", claudeDir)
	project := t.TempDir()
	const sessionID = "14141414-2222-3333-4444-555555555555"
	path := filepath.Join(claudeDir, "projects", ConvertToClaudeDirName(project), sessionID+".jsonl")
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	data := `{"sessionId":"` + sessionID + `","type":"user","message":{"role":"user","content":"before"}}` + "\nnot-json\n" + `{"sessionId":"` + sessionID + `","type":"assistant","message":{"role":"assistant","content":"after"}}` + "\n"
	if err := os.WriteFile(path, []byte(data), 0o600); err != nil {
		t.Fatal(err)
	}
	_, _, err := BuildClaudeToCodexHandoffPrompt(&Instance{Title: "malformed", ProjectPath: project, Tool: "claude", ClaudeSessionID: sessionID}, DefaultHandoffMaxChars)
	if err == nil || !strings.Contains(err.Error(), "malformed JSONL record") {
		t.Fatalf("malformed non-tail record error = %v, want refusal", err)
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
	for _, unwanted := range []string{"do not leak this", "summary records"} {
		if strings.Contains(prompt, unwanted) {
			t.Fatalf("prompt included unwanted %q:\n%s", unwanted, prompt)
		}
	}
	if info.MessageCount != 4 || info.IncludedCount != 4 || info.Truncated {
		t.Fatalf("info = %+v, want 4 readable conversation turns untruncated", info)
	}
}

func TestBuildClaudeToCodexHandoffPrompt_SkipsSidechainRecords(t *testing.T) {
	claudeDir := t.TempDir()
	t.Setenv("CLAUDE_CONFIG_DIR", claudeDir)

	project := t.TempDir()
	if resolved, err := filepath.EvalSymlinks(project); err == nil {
		project = resolved
	}
	sessionID := "cccccccc-dddd-eeee-ffff-000000000000"
	transcriptDir := filepath.Join(claudeDir, "projects", ConvertToClaudeDirName(project))
	if err := os.MkdirAll(transcriptDir, 0o755); err != nil {
		t.Fatal(err)
	}
	transcript := strings.Join([]string{
		`{"type":"user","message":{"role":"user","content":"main conversation GOLD ANCHOR"}}`,
		`{"type":"assistant","isSidechain":true,"message":{"role":"assistant","content":"sidechain subagent noise"}}`,
		`{"type":"assistant","message":{"role":"assistant","content":"main reply"}}`,
	}, "\n")
	if err := os.WriteFile(filepath.Join(transcriptDir, sessionID+".jsonl"), []byte(transcript), 0o644); err != nil {
		t.Fatal(err)
	}

	inst := &Instance{Title: "sidechain-test", ProjectPath: project, Tool: "claude", ClaudeSessionID: sessionID}
	prompt, info, err := BuildClaudeToCodexHandoffPrompt(inst, 32000)
	if err != nil {
		t.Fatalf("BuildClaudeToCodexHandoffPrompt: %v", err)
	}
	if strings.Contains(prompt, "sidechain subagent noise") {
		t.Fatalf("prompt leaked sidechain record:\n%s", prompt)
	}
	if !strings.Contains(prompt, "GOLD ANCHOR") || !strings.Contains(prompt, "main reply") {
		t.Fatalf("prompt missing main conversation:\n%s", prompt)
	}
	if info.MessageCount != 2 {
		t.Fatalf("info = %+v, want 2 main-conversation messages", info)
	}
}

func TestBuildClaudeToCodexHandoffPrompt_MaxCharsIsARealCeiling(t *testing.T) {
	claudeDir := t.TempDir()
	t.Setenv("CLAUDE_CONFIG_DIR", claudeDir)

	project := t.TempDir()
	if resolved, err := filepath.EvalSymlinks(project); err == nil {
		project = resolved
	}
	sessionID := "dddddddd-eeee-ffff-0000-111111111111"
	transcriptDir := filepath.Join(claudeDir, "projects", ConvertToClaudeDirName(project))
	if err := os.MkdirAll(transcriptDir, 0o755); err != nil {
		t.Fatal(err)
	}
	huge := strings.Repeat("filler ", 2000) + "NEWEST TAIL MARKER"
	transcript := `{"type":"user","message":{"role":"user","content":"` + huge + `"}}`
	if err := os.WriteFile(filepath.Join(transcriptDir, sessionID+".jsonl"), []byte(transcript), 0o644); err != nil {
		t.Fatal(err)
	}

	inst := &Instance{Title: "ceiling-test", ProjectPath: project, Tool: "claude", ClaudeSessionID: sessionID}
	maxChars := 500
	prompt, info, err := BuildClaudeToCodexHandoffPrompt(inst, maxChars)
	if err != nil {
		t.Fatalf("BuildClaudeToCodexHandoffPrompt: %v", err)
	}
	if !info.Truncated || info.IncludedCount != 1 {
		t.Fatalf("info = %+v, want truncated single message", info)
	}
	if !strings.Contains(prompt, "NEWEST TAIL MARKER") {
		t.Fatalf("prompt lost the newest tail:\n%s", prompt)
	}
	// The transcript body must respect the ceiling (plus small truncation banner).
	begin := strings.Index(prompt, "--- BEGIN TRANSFERRED TRANSCRIPT ---")
	end := strings.Index(prompt, "--- END TRANSFERRED TRANSCRIPT ---")
	if begin < 0 || end < 0 {
		t.Fatal("prompt missing transcript markers")
	}
	if body := end - begin; body > maxChars+200 {
		t.Fatalf("transcript body %d chars exceeds max %d", body, maxChars)
	}
}
