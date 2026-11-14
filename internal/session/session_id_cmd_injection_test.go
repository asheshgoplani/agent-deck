package session

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// evilSessionID breaks out of both a bare splice (`;`) and a double-quoted one
// (`$(...)`). Session ids are settable via `agent-deck session set ...` and
// arrive from disk/hook payloads, so the launch builders must quote them.
const evilSessionID = "abc; touch PWNED; $(touch PWNED)"

const (
	ordinarySessionID      = "3f2b8c1e-9a4d-4e7f-b6c5-0d1e2f3a4b5c"
	ordinaryCodexSessionID = "019f0000-0000-7000-8000-999999999999"
)

// sessionIDInjectionEnv isolates HOME and the tool config dirs under a temp dir
// and returns a fresh project directory.
func sessionIDInjectionEnv(t *testing.T) string {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("CLAUDE_CONFIG_DIR", filepath.Join(home, ".claude"))
	t.Setenv("COPILOT_CONFIG_DIR", filepath.Join(home, ".copilot"))
	ClearUserConfigCache()
	t.Cleanup(ClearUserConfigCache)
	project := filepath.Join(home, "project")
	if err := os.MkdirAll(project, 0o755); err != nil {
		t.Fatalf("mkdir project: %v", err)
	}
	return project
}

// runWithFakeTool executes cmdStr in a real bash in a temp dir with a stub
// `tool` first on PATH that records its argv, and returns that argv. It fails
// the test if the injected `touch PWNED` ran.
func runWithFakeTool(t *testing.T, tool, cmdStr string) []string {
	t.Helper()
	runDir := t.TempDir()
	binDir := t.TempDir()
	record := filepath.Join(t.TempDir(), "record")
	stub := "#!/bin/sh\nfor a in \"$@\"; do printf '%s\\n' \"$a\"; done > " + shellQuote(record) + "\n"
	if err := os.WriteFile(filepath.Join(binDir, tool), []byte(stub), 0o755); err != nil {
		t.Fatalf("write stub: %v", err)
	}
	cmd := exec.Command("bash", "-c", cmdStr)
	cmd.Dir = runDir
	cmd.Env = append(os.Environ(), "PATH="+binDir+":"+os.Getenv("PATH"))
	out, runErr := cmd.CombinedOutput()
	if _, err := os.Stat(filepath.Join(runDir, "PWNED")); err == nil {
		t.Errorf("injected command executed: PWNED created\ncmd: %s", cmdStr)
	}
	data, err := os.ReadFile(record)
	if err != nil {
		t.Fatalf("stub %s never ran: %v (run err %v, output %s)\ncmd: %s", tool, err, runErr, out, cmdStr)
	}
	return strings.Split(strings.TrimSuffix(string(data), "\n"), "\n")
}

func assertArgFollows(t *testing.T, args []string, flag, want, cmdStr string) {
	t.Helper()
	for k := 0; k+1 < len(args); k++ {
		if args[k] == flag && args[k+1] == want {
			return
		}
	}
	t.Errorf("argv = %q, want %s %q as one argument\ncmd: %s", args, flag, want, cmdStr)
}

func writeClaudeTranscriptFor(t *testing.T, project, sid string) {
	t.Helper()
	resolved, err := filepath.EvalSymlinks(project)
	if err != nil {
		resolved = project
	}
	writeConversation(t, os.Getenv("CLAUDE_CONFIG_DIR"), resolved, sid,
		`{"type":"user","sessionId":"x","message":{"role":"user","content":"hi"}}`+"\n")
}

func claudeInstanceWithID(t *testing.T, project, sid string) *Instance {
	t.Helper()
	inst := NewInstanceWithTool("sid-inject", project, "claude")
	inst.ClaudeSessionID = sid
	return inst
}

// buildClaudeResumeCommand, no transcript: `--session-id <id>`.
func TestSessionIDQuoting_ClaudeRestartSessionID(t *testing.T) {
	project := sessionIDInjectionEnv(t)
	inst := claudeInstanceWithID(t, project, evilSessionID)
	cmdStr := inst.buildClaudeResumeCommand()
	assertArgFollows(t, runWithFakeTool(t, "claude", cmdStr), "--session-id", evilSessionID, cmdStr)
}

// buildClaudeResumeCommand, transcript present: `--resume <id>`.
func TestSessionIDQuoting_ClaudeRestartResume(t *testing.T) {
	project := sessionIDInjectionEnv(t)
	writeClaudeTranscriptFor(t, project, evilSessionID)
	inst := claudeInstanceWithID(t, project, evilSessionID)
	cmdStr := inst.buildClaudeResumeCommand()
	assertArgFollows(t, runWithFakeTool(t, "claude", cmdStr), "--resume", evilSessionID, cmdStr)
}

// continue mode with an owned id resumes the recorded id.
func TestSessionIDQuoting_ClaudeContinueRecorded(t *testing.T) {
	project := sessionIDInjectionEnv(t)
	inst := claudeInstanceWithID(t, project, evilSessionID)
	if err := inst.SetClaudeOptions(&ClaudeOptions{SessionMode: "continue"}); err != nil {
		t.Fatal(err)
	}
	cmdStr := inst.buildClaudeCommand("claude")
	assertArgFollows(t, runWithFakeTool(t, "claude", cmdStr), "--resume", evilSessionID, cmdStr)
}

// resume mode, transcript present: `--resume <ResumeSessionID>`.
func TestSessionIDQuoting_ClaudeResumeMode(t *testing.T) {
	project := sessionIDInjectionEnv(t)
	writeClaudeTranscriptFor(t, project, evilSessionID)
	inst := claudeInstanceWithID(t, project, evilSessionID)
	if err := inst.SetClaudeOptions(&ClaudeOptions{SessionMode: "resume", ResumeSessionID: evilSessionID}); err != nil {
		t.Fatal(err)
	}
	cmdStr := inst.buildClaudeCommand("claude")
	assertArgFollows(t, runWithFakeTool(t, "claude", cmdStr), "--resume", evilSessionID, cmdStr)
}

// resume mode, no transcript: the own id is reused via `--session-id "<id>"`,
// where double quotes still let $(...) run.
func TestSessionIDQuoting_ClaudeResumeModeNoTranscript(t *testing.T) {
	project := sessionIDInjectionEnv(t)
	inst := claudeInstanceWithID(t, project, evilSessionID)
	if err := inst.SetClaudeOptions(&ClaudeOptions{SessionMode: "resume", ResumeSessionID: evilSessionID}); err != nil {
		t.Fatal(err)
	}
	cmdStr := inst.buildClaudeCommand("claude")
	assertArgFollows(t, runWithFakeTool(t, "claude", cmdStr), "--session-id", evilSessionID, cmdStr)
}

func TestSessionIDQuoting_Gemini(t *testing.T) {
	project := sessionIDInjectionEnv(t)
	inst := NewInstanceWithTool("sid-inject", project, "gemini")
	inst.GeminiSessionID = evilSessionID
	cmdStr := inst.buildGeminiCommand("gemini")
	assertArgFollows(t, runWithFakeTool(t, "gemini", cmdStr), "--resume", evilSessionID, cmdStr)
}

func TestSessionIDQuoting_OpenCodeV1(t *testing.T) {
	project := sessionIDInjectionEnv(t)
	orig := probeOpenCodeMajorVersion
	probeOpenCodeMajorVersion = func(*Instance) (int, bool) { return 1, true }
	t.Cleanup(func() { probeOpenCodeMajorVersion = orig })
	inst := NewInstanceWithTool("sid-inject", project, "opencode")
	inst.OpenCodeSessionID = evilSessionID
	cmdStr := inst.buildOpenCodeCommand("opencode")
	assertArgFollows(t, runWithFakeTool(t, "opencode", cmdStr), "-s", evilSessionID, cmdStr)
}

func TestSessionIDQuoting_Copilot(t *testing.T) {
	sessionIDInjectionEnv(t)
	dir := filepath.Join(getCopilotSessionStateDir(), evilSessionID)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "events.jsonl"), []byte(strings.Repeat("x", 9*1024)), 0o600); err != nil {
		t.Fatal(err)
	}
	inst := &Instance{Tool: "copilot", Command: "copilot", CopilotSessionID: evilSessionID}
	cmdStr := buildCopilotCommand(inst)
	assertArgFollows(t, runWithFakeTool(t, "copilot", cmdStr), "--resume", evilSessionID, cmdStr)
}

func TestSessionIDQuoting_CodexResume(t *testing.T) {
	inst, codexHome := newCodexGateInstance(t)
	seedCodexRolloutWithMeta(t, codexHome, evilSessionID, "user", "", false)
	inst.CodexSessionID = evilSessionID
	cmdStr := inst.buildCodexCommand("codex")
	assertArgFollows(t, runWithFakeTool(t, "codex", cmdStr), "resume", evilSessionID, cmdStr)
}

func TestSessionIDQuoting_CodexFork(t *testing.T) {
	inst, codexHome := newCodexGateInstance(t)
	mainSID := uniqueSID(t)
	seedCodexRolloutWithMeta(t, codexHome, mainSID, "user", "", false)
	seedCodexRolloutWithMeta(t, codexHome, evilSessionID, "subagent", mainSID, true)
	inst.CodexSessionID = evilSessionID
	cmdStr := inst.buildCodexCommand("codex")
	assertArgFollows(t, runWithFakeTool(t, "codex", cmdStr), "fork", evilSessionID, cmdStr)
}

// TestSessionIDQuoting_OrdinaryIDUnchanged pins that hardening leaves the
// command text for an ordinary UUID exactly as before.
func TestSessionIDQuoting_OrdinaryIDUnchanged(t *testing.T) {
	id := ordinarySessionID
	cases := []struct {
		name  string
		build func(t *testing.T) string
		want  string
	}{
		{"restart session-id", func(t *testing.T) string {
			return claudeInstanceWithID(t, sessionIDInjectionEnv(t), id).buildClaudeResumeCommand()
		}, "exec claude --session-id " + id},
		{"restart resume", func(t *testing.T) string {
			project := sessionIDInjectionEnv(t)
			writeClaudeTranscriptFor(t, project, id)
			return claudeInstanceWithID(t, project, id).buildClaudeResumeCommand()
		}, "exec claude --resume " + id},
		{"continue recorded", func(t *testing.T) string {
			inst := claudeInstanceWithID(t, sessionIDInjectionEnv(t), id)
			_ = inst.SetClaudeOptions(&ClaudeOptions{SessionMode: "continue"})
			return inst.buildClaudeCommand("claude")
		}, " claude --resume " + id + " "},
		{"resume mode", func(t *testing.T) string {
			project := sessionIDInjectionEnv(t)
			writeClaudeTranscriptFor(t, project, id)
			inst := claudeInstanceWithID(t, project, id)
			_ = inst.SetClaudeOptions(&ClaudeOptions{SessionMode: "resume", ResumeSessionID: id})
			return inst.buildClaudeCommand("claude")
		}, " claude --resume " + id + " "},
		{"resume mode no transcript", func(t *testing.T) string {
			inst := claudeInstanceWithID(t, sessionIDInjectionEnv(t), id)
			_ = inst.SetClaudeOptions(&ClaudeOptions{SessionMode: "resume", ResumeSessionID: id})
			return inst.buildClaudeCommand("claude")
		}, ` claude --session-id "` + id + `" `},
		{"gemini", func(t *testing.T) string {
			inst := NewInstanceWithTool("sid-plain", sessionIDInjectionEnv(t), "gemini")
			inst.GeminiSessionID = id
			return inst.buildGeminiCommand("gemini")
		}, "gemini --resume " + id},
		{"opencode", func(t *testing.T) string {
			orig := probeOpenCodeMajorVersion
			probeOpenCodeMajorVersion = func(*Instance) (int, bool) { return 1, true }
			t.Cleanup(func() { probeOpenCodeMajorVersion = orig })
			inst := NewInstanceWithTool("sid-plain", sessionIDInjectionEnv(t), "opencode")
			inst.OpenCodeSessionID = id
			return inst.buildOpenCodeCommand("opencode")
		}, "opencode -s " + id},
		{"copilot", func(t *testing.T) string {
			sessionIDInjectionEnv(t)
			dir := filepath.Join(getCopilotSessionStateDir(), id)
			_ = os.MkdirAll(dir, 0o755)
			_ = os.WriteFile(filepath.Join(dir, "events.jsonl"), []byte(strings.Repeat("x", 9*1024)), 0o600)
			return buildCopilotCommand(&Instance{Tool: "copilot", Command: "copilot", CopilotSessionID: id})
		}, "copilot --resume " + id},
		{"codex resume", func(t *testing.T) string {
			inst, codexHome := newCodexGateInstance(t)
			seedCodexRolloutWithMeta(t, codexHome, ordinaryCodexSessionID, "user", "", false)
			inst.CodexSessionID = ordinaryCodexSessionID
			return inst.buildCodexCommand("codex")
		}, " resume " + ordinaryCodexSessionID},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := tc.build(t)
			if !strings.Contains(got, tc.want) {
				t.Errorf("command = %q, want it to contain %q", got, tc.want)
			}
		})
	}
}

func TestSessionIDQuoting_CrushResume(t *testing.T) {
	sessionIDInjectionEnv(t)
	raw, err := MarshalToolOptions(&CrushOptions{ResumeSessionID: evilSessionID})
	if err != nil {
		t.Fatal(err)
	}
	inst := &Instance{Tool: "crush", Command: "crush", ToolOptionsJSON: raw}
	cmdStr := inst.buildCrushCommand("crush")
	assertArgFollows(t, runWithFakeTool(t, "crush", cmdStr), "--session", evilSessionID, cmdStr)
}
