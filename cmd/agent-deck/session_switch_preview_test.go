package main

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/asheshgoplani/agent-deck/internal/session"
)

const sessionSwitchPreviewHelperEnv = "AGENT_DECK_SESSION_SWITCH_PREVIEW_HELPER_PROCESS"

// switchPreviewHelperEnv holds isolated XDG paths used to run the helper process.
type switchPreviewHelperEnv struct {
	home      string
	xdgConfig string
	xdgData   string
	xdgCache  string
}

func setupSessionSwitchPreviewFixture(t *testing.T, withSessionID bool) (switchPreviewHelperEnv, string) {
	t.Helper()

	home := t.TempDir()
	xdgConfig := filepath.Join(home, ".config")
	xdgData := filepath.Join(home, ".local", "share")
	xdgCache := filepath.Join(home, ".cache")

	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", xdgConfig)
	t.Setenv("XDG_DATA_HOME", xdgData)
	t.Setenv("XDG_CACHE_HOME", xdgCache)
	t.Setenv("AGENTDECK_PROFILE", "default")
	session.ClearUserConfigCache()

	cfgPath, err := session.GetUserConfigPath()
	if err != nil {
		t.Fatalf("resolve user config path: %v", err)
	}
	if err := os.MkdirAll(filepath.Dir(cfgPath), 0o700); err != nil {
		t.Fatalf("mkdir config dir: %v", err)
	}
	if err := os.WriteFile(cfgPath, []byte(`
[profiles.personal.claude]
config_dir = "~/.claude-personal"

[profiles.work.claude]
config_dir = "~/.claude-work"

[profiles.workcodex.codex]
config_dir = "~/.codex-work"
`), 0o600); err != nil {
		t.Fatalf("write config: %v", err)
	}

	title := "preview-cmd-session"
	project := filepath.Join(home, "project")
	if err := os.MkdirAll(project, 0o700); err != nil {
		t.Fatalf("mkdir project: %v", err)
	}

	inst := session.NewInstance(title, project)
	inst.Tool = "claude"
	inst.Account = "personal"
	if withSessionID {
		inst.ClaudeSessionID = "11111111-2222-3333-4444-555555555555"
	}

	storage, err := session.NewStorageWithProfile(session.DefaultProfile)
	if err != nil {
		t.Fatalf("create storage: %v", err)
	}
	if err := storage.Save([]*session.Instance{inst}); err != nil {
		t.Fatalf("seed session: %v", err)
	}
	loaded, _, err := storage.LoadWithGroups()
	if err != nil {
		t.Fatalf("reload seeded storage: %v", err)
	}
	if got := len(loaded); got != 1 {
		t.Fatalf("seeded storage has %d instances, want 1", got)
	}
	if loaded[0].Title != title {
		t.Fatalf("seeded storage title=%q want %q", loaded[0].Title, title)
	}

	session.ClearUserConfigCache()
	return switchPreviewHelperEnv{
		home:      home,
		xdgConfig: xdgConfig,
		xdgData:   xdgData,
		xdgCache:  xdgCache,
	}, title
}

func runSessionSwitchPreviewHelper(t *testing.T, env switchPreviewHelperEnv, args ...string) (string, int) {
	t.Helper()

	cmdArgs := append([]string{"-test.run=^TestSessionSwitchPreviewHelperProcess$", "--"}, args...)
	cmd := exec.Command(os.Args[0], cmdArgs...)

	envOut := os.Environ()
	filtered := make([]string, 0, len(envOut)+5)
	for _, e := range envOut {
		if strings.HasPrefix(e, "HOME=") ||
			strings.HasPrefix(e, "XDG_CONFIG_HOME=") ||
			strings.HasPrefix(e, "XDG_DATA_HOME=") ||
			strings.HasPrefix(e, "XDG_CACHE_HOME=") ||
			strings.HasPrefix(e, "AGENTDECK_PROFILE=") ||
			strings.HasPrefix(e, sessionSwitchPreviewHelperEnv+"=") {
			continue
		}
		filtered = append(filtered, e)
	}
	filtered = append(filtered,
		sessionSwitchPreviewHelperEnv+"=1",
		"AGENT_DECK_TASK6_HELPER_PROCESS=1",
		"HOME="+env.home,
		"XDG_CONFIG_HOME="+env.xdgConfig,
		"XDG_DATA_HOME="+env.xdgData,
		"XDG_CACHE_HOME="+env.xdgCache,
		"AGENTDECK_PROFILE=default",
	)
	cmd.Env = filtered

	out, err := cmd.CombinedOutput()
	exitCode := 0
	if err != nil {
		exit, ok := err.(*exec.ExitError)
		if !ok {
			t.Fatalf("helper process failed to run: %v: %s", err, string(out))
		}
		exitCode = exit.ExitCode()
	}
	return string(out), exitCode
}

func TestSessionSwitchPreviewHelperProcess(t *testing.T) {
	if os.Getenv(sessionSwitchPreviewHelperEnv) != "1" {
		return
	}

	args := os.Args
	for len(args) > 0 && args[0] != "--" {
		args = args[1:]
	}
	if len(args) > 0 {
		args = args[1:]
	}
	if len(args) < 2 {
		os.Exit(2)
	}
	if args[0] != "session" || args[1] != "switch-preview" {
		os.Exit(2)
	}
	handleSession(session.DefaultProfile, args[1:])
	os.Exit(0)
}

type sessionSwitchPreviewJSON struct {
	Capability string `json:"capability"`
	Refusal    *struct {
		Code    string `json:"code"`
		Message string `json:"message"`
	} `json:"refusal,omitempty"`
	SourceTitle           string `json:"source_title"`
	SourceTool            string `json:"source_tool"`
	SourceAccount         string `json:"source_account"`
	SourceAccountStatus   string `json:"source_account_status"`
	TargetHarness         string `json:"target_harness"`
	TargetAccount         string `json:"target_account"`
	TargetAccountStatus   string `json:"target_account_status"`
	SourceSessionID       string `json:"source_session_id"`
	TranscriptBudgetBytes int    `json:"transcript_budget_bytes"`
}

func TestSessionSwitchPreviewJSON_NativeResume(t *testing.T) {
	env, title := setupSessionSwitchPreviewFixture(t, true)

	out, code := runSessionSwitchPreviewHelper(t, env, "session", "switch-preview", title, "--to-account", "work", "--json")
	if code != 0 {
		t.Fatalf("session switch-preview failed with code %d: %s", code, out)
	}

	var payload sessionSwitchPreviewJSON
	if err := json.Unmarshal([]byte(out), &payload); err != nil {
		t.Fatalf("parse json: %v\nout=%q", err, out)
	}

	if payload.SourceTitle != title {
		t.Fatalf("source_title=%q want %q", payload.SourceTitle, title)
	}
	if payload.Capability != string(session.CapabilityNativeResume) {
		t.Fatalf("capability=%q want %q", payload.Capability, session.CapabilityNativeResume)
	}
	if payload.Refusal != nil {
		t.Fatalf("unexpected refusal: %#v", payload.Refusal)
	}
	if payload.TargetHarness != "claude" {
		t.Fatalf("target_harness=%q want claude", payload.TargetHarness)
	}
	if payload.TargetAccount != "work" {
		t.Fatalf("target_account=%q want work", payload.TargetAccount)
	}
}

func TestSessionSwitchPreviewJSON_TranscriptTailRequiresSessionID(t *testing.T) {
	env, title := setupSessionSwitchPreviewFixture(t, false)

	out, code := runSessionSwitchPreviewHelper(t, env, "session", "switch-preview", title, "--to-harness", "codex", "--json")
	if code == 0 {
		t.Fatalf("switch-preview returned zero for refusal payload: code=%d out=%s", code, out)
	}

	var payload sessionSwitchPreviewJSON
	if err := json.Unmarshal([]byte(out), &payload); err != nil {
		t.Fatalf("parse json: %v\nout=%q", err, out)
	}
	if payload.Capability != string(session.CapabilityTranscriptTail) {
		t.Fatalf("capability=%q want %q", payload.Capability, session.CapabilityTranscriptTail)
	}
	if payload.Refusal == nil || payload.Refusal.Code != "no-session-id" {
		t.Fatalf("expected no-session-id refusal, got %#v", payload.Refusal)
	}
}

func TestSessionSwitchPreviewJSON_UnknownTargetAccount(t *testing.T) {
	env, title := setupSessionSwitchPreviewFixture(t, true)

	out, code := runSessionSwitchPreviewHelper(t, env, "session", "switch-preview", title, "--to-account", "ghost", "--json")
	if code != 1 {
		t.Fatalf("switch-preview refusal exit code = %d, want 1; out=%s", code, out)
	}

	var payload sessionSwitchPreviewJSON
	if err := json.Unmarshal([]byte(out), &payload); err != nil {
		t.Fatalf("parse json: %v\nout=%q", err, out)
	}
	if payload.Refusal == nil || payload.Refusal.Code != "unknown-account" || !strings.Contains(payload.Refusal.Message, "ghost") {
		t.Fatalf("expected unknown-account refusal payload, got %#v", payload.Refusal)
	}
	if payload.TargetAccount != "ghost" {
		t.Fatalf("target_account=%q want ghost", payload.TargetAccount)
	}
}

func TestSessionSwitchPreviewJSON_RespectsMaxChars(t *testing.T) {
	env, title := setupSessionSwitchPreviewFixture(t, true)

	out, code := runSessionSwitchPreviewHelper(t, env, "session", "switch-preview", title, "--to-harness", "codex", "--json", "--max-chars", "1024")
	if code != 0 {
		t.Fatalf("unexpected non-zero code %d: %s", code, out)
	}
	var payload sessionSwitchPreviewJSON
	if err := json.Unmarshal([]byte(out), &payload); err != nil {
		t.Fatalf("parse json: %v\nout=%q", err, out)
	}
	if payload.TranscriptBudgetBytes != 1024 {
		t.Fatalf("transcript_budget_bytes=%d want 1024", payload.TranscriptBudgetBytes)
	}
	if payload.Refusal != nil {
		t.Fatalf("did not expect refusal in max-chars test, got %#v", payload.Refusal)
	}
}

func TestSessionSwitchPreview_Human_RefusalExitsNonZero(t *testing.T) {
	env, title := setupSessionSwitchPreviewFixture(t, false)

	out, code := runSessionSwitchPreviewHelper(t, env, "session", "switch-preview", title, "--to-harness", "codex")
	if code == 0 {
		t.Fatalf("expected non-zero exit for refusal, got code %d", code)
	}
	if !strings.Contains(out, "REFUSED [no-session-id]") {
		t.Fatalf("expected refusal line in human output, got: %s", out)
	}
}
