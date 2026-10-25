package session

import (
	"bytes"
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/asheshgoplani/agent-deck/internal/tmux"
)

// LOC-4: inline env values from config / `restart --env` must not appear in
// the pane command line (argv of tmux, systemd-run and the pane's bash, visible
// in ps) nor in restart/launch log lines.

func loc4CaptureLogs(t *testing.T) *bytes.Buffer {
	t.Helper()
	buf := &bytes.Buffer{}
	h := slog.NewJSONHandler(buf, &slog.HandlerOptions{Level: slog.LevelDebug})
	origSession, origMCP := sessionLog, mcpLog
	sessionLog, mcpLog = slog.New(h), slog.New(h)
	t.Cleanup(func() { sessionLog, mcpLog = origSession, origMCP })
	return buf
}

func loc4UseToolEnv(t *testing.T, env map[string]string) {
	t.Helper()
	t.Setenv("HOME", t.TempDir())
	userConfigCacheMu.Lock()
	orig := userConfigCache
	userConfigCache = &UserConfig{
		Tools: map[string]ToolDef{"loc4tool": {Command: "true", Env: env}},
		MCPs:  make(map[string]MCPDef),
	}
	userConfigCacheMu.Unlock()
	t.Cleanup(func() {
		userConfigCacheMu.Lock()
		userConfigCache = orig
		userConfigCacheMu.Unlock()
	})
}

// loc4RunPrefix runs `<prefix> printf %s "$KEY"` under bash and returns stdout,
// proving the env still reaches the launched process.
func loc4RunPrefix(t *testing.T, prefix, key string) string {
	t.Helper()
	out, err := exec.Command("bash", "-c", prefix+`printf %s "$`+key+`"`).CombinedOutput()
	if err != nil {
		t.Fatalf("run prefix: %v\n%s\nprefix: %s", err, out, prefix)
	}
	return string(out)
}

func TestLOC4_InlineToolEnvNotInCommandLine(t *testing.T) {
	const secret = "loc4-tool-SECRET 'quoted' $HOME"
	loc4UseToolEnv(t, map[string]string{"LOC4_API_KEY": secret})

	inst := &Instance{ID: "loc4-tool-inst", Title: "loc4", Tool: "loc4tool", ProjectPath: t.TempDir()}
	prefix := inst.buildEnvSourceCommand()
	if strings.Contains(prefix, "loc4-tool-SECRET") {
		t.Fatalf("inline [tools.X].env value is in the pane command line:\n%s", prefix)
	}
	if got := loc4RunPrefix(t, prefix, "LOC4_API_KEY"); got != secret {
		t.Fatalf("env value did not reach the process: got %q, want %q", got, secret)
	}
	if again := inst.buildEnvSourceCommand(); again != prefix {
		t.Errorf("command is not deterministic across builds:\n%s\n%s", prefix, again)
	}

	dir, err := runtimeDataPath("launch-env")
	if err != nil {
		t.Fatalf("runtimeDataPath: %v", err)
	}
	entries, err := os.ReadDir(dir)
	if err != nil || len(entries) == 0 {
		t.Fatalf("no launch env file written in %s: %v", dir, err)
	}
	if info, _ := os.Stat(dir); info.Mode().Perm() != 0o700 {
		t.Errorf("launch env dir mode = %v, want 0700", info.Mode().Perm())
	}
	for _, e := range entries {
		info, _ := e.Info()
		if info.Mode().Perm() != 0o600 {
			t.Errorf("launch env file %s mode = %v, want 0600", e.Name(), info.Mode().Perm())
		}
	}
}

func TestLOC4_RestartEnvPrefixNotInCommandLine(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	const secret = "loc4-restart-SECRET"
	inst := &Instance{ID: "loc4-restart-inst", restartEnv: map[string]string{"LOC4_TOKEN": secret}}
	prefix := inst.buildRestartEnvPrefix()
	if strings.Contains(prefix, secret) {
		t.Fatalf("restart --env value is in the command line: %s", prefix)
	}
	if got := loc4RunPrefix(t, prefix, "LOC4_TOKEN"); got != secret {
		t.Fatalf("restart env did not reach the process: got %q", got)
	}
}

// Remote sessions run the command on another host where a local file does not
// exist, so they must keep the inline form (non-breaking).
func TestLOC4_SSHSessionKeepsInlineEnv(t *testing.T) {
	loc4UseToolEnv(t, map[string]string{"LOC4_API_KEY": "remote-value"})
	inst := &Instance{ID: "loc4-ssh-inst", Title: "loc4", Tool: "loc4tool", ProjectPath: t.TempDir(), SSHHost: "example-host"}
	if prefix := inst.buildEnvSourceCommand(); !strings.Contains(prefix, "export LOC4_API_KEY='remote-value'") {
		t.Fatalf("ssh session lost its inline env:\n%s", prefix)
	}
}

func TestLOC4_InvalidToolEnvKeyRejected(t *testing.T) {
	loc4UseToolEnv(t, map[string]string{
		"A=1; touch LOC4_PWNED; B": "x",
		"GOOD_KEY":                 "ok",
	})
	buf := loc4CaptureLogs(t)
	inst := &Instance{Tool: "loc4tool"}
	got := inst.getToolInlineEnv()
	if strings.Contains(got, "LOC4_PWNED") {
		t.Fatalf("invalid env key was spliced into the shell command: %s", got)
	}
	if !strings.Contains(got, "export GOOD_KEY='ok'") {
		t.Fatalf("valid key dropped: %s", got)
	}
	if !strings.Contains(buf.String(), "invalid_env_key_skipped") {
		t.Errorf("invalid key was dropped silently; want a warning:\n%s", buf.String())
	}
}

// TestLOC4_RestartLogsAndArgvOmitEnvValues drives a real restart (isolated
// tmux from TestMain) and checks the restart log lines and the live pane's
// start command for the secret.
func TestLOC4_RestartLogsAndArgvOmitEnvValues(t *testing.T) {
	skipIfNoTmuxBinary(t)

	projectDir := t.TempDir()
	stubDir := t.TempDir()
	seenLog := filepath.Join(t.TempDir(), "seen.log")
	stub := "#!/bin/sh\nif [ \"$1\" = \"session\" ]; then printf '[]\\n'; exit 0; fi\nprintf '%s' \"$LOC4_SECRET_TOKEN\" > \"$LOC4_SEEN\"\nsleep 30\n"
	if err := os.WriteFile(filepath.Join(stubDir, "opencode"), []byte(stub), 0o755); err != nil {
		t.Fatalf("write stub: %v", err)
	}
	stubPATH := stubDir + string(os.PathListSeparator) + os.Getenv("PATH")
	t.Setenv("PATH", stubPATH)

	tmuxName := fmt.Sprintf("agentdeck-loc4-restart-%d", time.Now().UnixNano())
	if out, err := exec.Command("tmux", "new-session", "-d", "-s", tmuxName, "-c", projectDir, "sleep 30").CombinedOutput(); err != nil {
		t.Fatalf("start isolated tmux session: %v (%s)", err, out)
	}
	t.Cleanup(func() { _ = exec.Command("tmux", "kill-session", "-t", tmuxName).Run() })

	inst := NewInstanceWithTool("loc4-restart", projectDir, "opencode")
	inst.ID = "loc4-restart-instance"
	inst.OpenCodeSessionID = "ses_LOC4"
	inst.tmuxSession = tmux.ReconnectSessionLazy(tmuxName, tmuxName, projectDir, "opencode", "waiting")

	buf := loc4CaptureLogs(t)
	const secret = "loc4-live-SECRET-token"
	if err := inst.RestartWithEnv(map[string]string{
		"LOC4_SECRET_TOKEN": secret,
		"LOC4_SEEN":         seenLog,
		"PATH":              stubPATH,
	}); err != nil {
		t.Fatalf("RestartWithEnv: %v", err)
	}

	var seen []byte
	var err error
	for deadline := time.Now().Add(5 * time.Second); time.Now().Before(deadline); time.Sleep(20 * time.Millisecond) {
		if seen, err = os.ReadFile(seenLog); err == nil && len(seen) > 0 {
			break
		}
	}
	if string(seen) != secret {
		t.Fatalf("restarted process did not receive the env value: got %q (err %v)", seen, err)
	}

	if strings.Contains(buf.String(), secret) {
		t.Errorf("restart logs leaked the env value:\n%s", buf.String())
	}
	paneCmd, err := exec.Command("tmux", "display-message", "-p", "-t", tmuxName+":", "#{pane_start_command}").Output()
	if err != nil {
		t.Fatalf("read pane start command: %v", err)
	}
	if strings.Contains(string(paneCmd), secret) {
		t.Errorf("pane start command (argv) carries the env value:\n%s", paneCmd)
	}
}

var loc4LaunchEnvSourcePattern = regexp.MustCompile(`\{ if \[ -f ('[^']*/launch-env/[^']*'|[^\s']*/launch-env/[^\s']*) \]; then source [^;]*; else echo '[^']*' >&2; fi; \}`)

// expandLaunchEnvSources inlines every `source <launch-env file>` fragment of
// cmd with the file's content, so tests that pin the ORDER and quoting of the
// env prelude keep asserting on the exports the shell will actually run.
func expandLaunchEnvSources(cmd string) string {
	return loc4LaunchEnvSourcePattern.ReplaceAllStringFunc(cmd, func(m string) string {
		path := strings.Trim(loc4LaunchEnvSourcePattern.FindStringSubmatch(m)[1], "'")
		body, err := os.ReadFile(path)
		if err != nil {
			return m
		}
		return strings.TrimSuffix(string(body), "\n")
	})
}
